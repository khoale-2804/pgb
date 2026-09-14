package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"

	"github.com/khoale-2804/pgb/gen"
)

// TestStandaloneMatchesPlugin is the editions parity gate: the standalone
// frontend (this command: oliphant DDL -> in-process request) must produce
// BYTE-IDENTICAL output to the sqlc plugin frontend (canned sqlc request
// captured in testdata/golden/plugin_request.pb, same schema.sql). If this
// test fails, the two editions have drifted.
func TestStandaloneMatchesPlugin(t *testing.T) {
	root := repoRoot(t)
	goldenDir := filepath.Join(root, "testdata", "golden")

	pb, err := os.ReadFile(filepath.Join(goldenDir, "plugin_request.pb"))
	if err != nil {
		t.Fatal(err)
	}
	var canned plugin.GenerateRequest
	if err := proto.Unmarshal(pb, &canned); err != nil {
		t.Fatal(err)
	}
	// sqlc resolves the captured relative schema paths against the config
	// dir (testdata/golden); rewrite them to absolute for the same files so
	// Enrich runs identically on both sides.
	for i, p := range canned.GetSettings().GetSchema() {
		canned.Settings.Schema[i] = filepath.Join(goldenDir, p)
	}

	expected, err := gen.Generate(context.Background(), &canned)
	if err != nil {
		t.Fatal(err)
	}

	// Standalone frontend: parse the same schema.sql, reuse the canned
	// request's options so only the FRONTEND differs, not the options.
	schemaFiles, err := expandSchemas([]string{filepath.Join(goldenDir, "schema.sql")})
	if err != nil {
		t.Fatal(err)
	}
	req, err := buildRequest(schemaFiles, "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	req.PluginOptions = canned.GetPluginOptions()
	req.SqlcVersion = canned.GetSqlcVersion()

	actual, err := gen.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]byte{}
	for _, f := range expected.GetFiles() {
		want[f.GetName()] = f.GetContents()
	}
	gotNames := []string{}
	if os.Getenv("PG_DUMP") != "" {
		for _, side := range []struct {
			name string
			resp *plugin.GenerateResponse
		}{{"exp", expected}, {"act", actual}} {
			dir := filepath.Join("/tmp/pg-parity", side.name)
			os.MkdirAll(dir, 0o755)
			for _, f := range side.resp.GetFiles() {
				os.WriteFile(filepath.Join(dir, f.GetName()), f.GetContents(), 0o644)
			}
		}
	}
	for _, f := range actual.GetFiles() {
		gotNames = append(gotNames, f.GetName())
		exp, ok := want[f.GetName()]
		if !ok {
			t.Errorf("standalone emitted extra file %s (plugin did not)", f.GetName())
			continue
		}
		if !bytes.Equal(exp, f.GetContents()) {
			t.Errorf("file %s differs between editions:\n--- plugin ---\n%s\n--- standalone ---\n%s",
				f.GetName(), firstDiff(exp, f.GetContents()), "")
		}
	}
	sort.Strings(gotNames)
	for name := range want {
		found := false
		for _, g := range gotNames {
			if g == name {
				found = true
			}
		}
		if !found {
			t.Errorf("standalone is missing file %s (plugin emitted it)", name)
		}
	}
	t.Logf("standalone parity: %d files, byte-identical", len(gotNames))
}

// firstDiff surfaces the first differing line pair for debuggability.
func firstDiff(a, b []byte) string {
	al := bytes.Split(a, []byte("\n"))
	bl := bytes.Split(b, []byte("\n"))
	for i := 0; i < len(al) && i < len(bl); i++ {
		if !bytes.Equal(al[i], bl[i]) {
			return string(al[i])
		}
	}
	if len(al) != len(bl) {
		return "(length differs)"
	}
	return "(?)"
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
