// TestSQLCPluginIntegration is the end-to-end proof for the pgb sqlc plugin:
// it builds cmd/sqlc-gen-pgb, points a sqlc config at that binary, runs
// `sqlc generate` over the golden fixture, and asserts the emitted package
// exists, honors pgb:skip, and compiles.
//
// Skipped (not failed) when the `sqlc` binary is absent from PATH.
package gen_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// findSQLC resolves the sqlc binary: PATH first, then ~/go/bin/sqlc (the
// usual `go install` location when ~/go/bin is not on PATH).
func findSQLC(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("sqlc"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, "go", "bin", "sqlc")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Skip("sqlc binary not found in PATH (or ~/go/bin)")
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), ".."))
}

func TestSQLCPluginIntegration(t *testing.T) {
	sqlcBin := findSQLC(t)
	root := repoRoot(t)
	goldenDir := filepath.Join(root, "testdata", "golden")
	pluginOut := filepath.Join(goldenDir, "gen-plugin")

	// Start from a clean output dir; leave the tree clean on the way out.
	if err := os.RemoveAll(pluginOut); err != nil {
		t.Fatalf("remove stale gen-plugin: %v", err)
	}
	defer os.RemoveAll(pluginOut)

	// 1. Build the plugin binary into a temp dir (absolute path keeps the
	//    variant config free of cwd assumptions; no spaces in t.TempDir paths).
	binPath := filepath.Join(t.TempDir(), "sqlc-gen-pgb")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/sqlc-gen-pgb")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/sqlc-gen-pgb: %v\n%s", err, out)
	}

	// 2. Variant config: same golden fixture, plugin cmd pointed at the
	//    prebuilt binary instead of `go run`. Must live in testdata/golden so
	//    schema/queries/out paths resolve; cleaned up after the test.
	base, err := os.ReadFile(filepath.Join(goldenDir, "sqlc-plugin.yaml"))
	if err != nil {
		t.Fatalf("read sqlc-plugin.yaml: %v", err)
	}
	const runRef = "cmd: \"sqlc-gen-pgb\""
	variant := strings.Replace(string(base), runRef, "cmd: \""+binPath+"\"", 1)
	if variant == string(base) {
		t.Fatalf("sqlc-plugin.yaml no longer contains %q — update this test", runRef)
	}
	cfgPath := filepath.Join(goldenDir, "sqlc-plugin.gen.yaml")
	if err := os.WriteFile(cfgPath, []byte(variant), 0o644); err != nil {
		t.Fatalf("write variant config: %v", err)
	}
	defer os.Remove(cfgPath)

	// 3. Run sqlc generate. Must exit 0.
	var runLog bytes.Buffer
	run := exec.Command(sqlcBin, "generate", "-f", "sqlc-plugin.gen.yaml")
	run.Dir = goldenDir
	run.Stdout, run.Stderr = &runLog, &runLog
	if err := run.Run(); err != nil {
		t.Fatalf("sqlc generate: %v\n%s", err, runLog.String())
	}

	// 4. Emitted files: models.gen.go present, at least one <table>.gen.go
	//    (pass B) and one <table>_repo.gen.go (pass C); no db.go (pgb
	//    replaces gen:go) and no queries.gen.go (pass A wrappers omitted in
	//    this slice); pgb:skip tables produce no files at all.
	entries, err := os.ReadDir(pluginOut)
	if err != nil {
		t.Fatalf("sqlc generate produced no gen-plugin dir: %v\n%s", err, runLog.String())
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	has := func(suffix string) bool {
		for _, n := range names {
			if n == suffix || strings.HasSuffix(n, suffix) {
				return true
			}
		}
		return false
	}
	if !has("models.gen.go") {
		t.Errorf("models.gen.go missing; got %v", names)
	}
	builderRe := regexp.MustCompile(`^[a-z0-9_]+\.gen\.go$`)
	repoRe := regexp.MustCompile(`^[a-z0-9_]+_repo\.gen\.go$`)
	var builders, statics int
	for _, n := range names {
		if n != "models.gen.go" && builderRe.MatchString(n) {
			builders++
		}
		if repoRe.MatchString(n) {
			statics++
		}
	}
	if builders == 0 {
		t.Errorf("no <table>.gen.go builder files; got %v", names)
	}
	if statics == 0 {
		t.Errorf("no <table>_repo.gen.go static files; got %v", names)
	}
	if has("db.go") {
		t.Errorf("db.go emitted — pgb replaces gen:go, stock files are out of scope")
	}
	if has("queries.gen.go") {
		t.Errorf("queries.gen.go emitted — pass A wrappers are not in this slice")
	}
	for _, n := range names {
		if strings.HasPrefix(n, "pgb_ignored") {
			t.Errorf("pgb:skip table leaked into output: %s", n)
		}
	}
	// skippable_partner is NOT skipped (it covers identity BY DEFAULT —
	// COVERAGE row 3): it must generate model/builder/statics files.
	skippable := false
	for _, n := range names {
		if strings.HasPrefix(n, "skippable_partner") {
			skippable = true
			break
		}
	}
	if !skippable {
		t.Errorf("skippable_partner (identity BY DEFAULT, not skipped) generated no files; got %v", names)
	}

	// 5. The emitted package must compile. It imports pgtype, uuid (uuid
	//    columns) and pgvector (products.embedding pgb:type directive), and
	//    pgvector is not a module dependency — so build once with -mod=mod,
	//    letting go add the missing requires/sums, then restore go.mod and
	//    go.sum so the test never leaves the tree dirty.
	modPath := filepath.Join(root, "go.mod")
	sumPath := filepath.Join(root, "go.sum")
	mod0, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	sum0, _ := os.ReadFile(sumPath) // go.sum may legitimately be absent
	defer func() {
		os.WriteFile(modPath, mod0, 0o644)
		os.WriteFile(sumPath, sum0, 0o644)
	}()

	var compileLog bytes.Buffer
	compile := exec.Command("go", "build", "-mod=mod", "./testdata/golden/gen-plugin/...")
	compile.Dir = root
	compile.Stdout, compile.Stderr = &compileLog, &compileLog
	if err := compile.Run(); err != nil {
		t.Fatalf("emitted package does not compile: %v\n%s", err, compileLog.String())
	}
}
