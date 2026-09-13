// TestGolden pins the plugin's full output byte-for-byte: it replays the real
// plugin.GenerateRequest sqlc sent for the golden fixture (captured on stdin
// into testdata/golden/plugin_request.pb — the process plugin wire format is
// exactly that message, per plugin-sdk-go internal/rpc) through gen.Generate
// and compares every emitted file against testdata/snapshots/<name>.
//
// Regenerating the fixtures (after ANY change to schema.sql, queries.sql, the
// sqlc version, or the codegen itself):
//
//	# 1. re-capture the request (build first: make plugin)
//	sed "s|cmd: \"sqlc-gen-pgb\"|cmd: /tmp/pgb-capture.sh|" \
//	  testdata/golden/sqlc-plugin.yaml > /tmp/cap.yaml
//	printf '#!/bin/sh\ncat > %s/testdata/golden/plugin_request.pb\nexit 0\n' \
//	  "$(pwd)" > /tmp/pgb-capture.sh && chmod +x /tmp/pgb-capture.sh
//	(cd testdata/golden && sqlc generate -f /tmp/cap.yaml)
//	# 2. rewrite the snapshots
//	go test ./gen -run TestGolden -update
//
// A snapshot diff then means output changed — update only deliberately.
package gen_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"google.golang.org/protobuf/proto"

	"github.com/khoale-2804/pgb/gen"
)

var updateSnapshots = flag.Bool("update", false, "rewrite testdata/snapshots/ from the current Generate output")

func TestGolden(t *testing.T) {
	root := repoRoot(t)
	reqPath := filepath.Join(root, "testdata", "golden", "plugin_request.pb")
	data, err := os.ReadFile(reqPath)
	if err != nil {
		t.Fatalf("read captured plugin request (%s): %v — re-capture per golden_test.go's header comment", reqPath, err)
	}
	var req plugin.GenerateRequest
	if err := proto.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal plugin request: %v", err)
	}

	// Enrich (the DDL extraction pass) resolves the request's schema paths
	// relative to the process cwd — the plugin contract — so Generate must
	// run from the fixture directory, exactly like a real sqlc invocation.
	t.Chdir(filepath.Join(root, "testdata", "golden"))

	resp, err := gen.Generate(context.Background(), &req)
	if err != nil {
		t.Fatalf("gen.Generate: %v", err)
	}
	if len(resp.Files) == 0 {
		t.Fatal("gen.Generate emitted no files")
	}

	// Determinism: a second run must be byte-identical.
	resp2, err := gen.Generate(context.Background(), &req)
	if err != nil {
		t.Fatalf("gen.Generate (second run): %v", err)
	}
	if len(resp.Files) != len(resp2.Files) {
		t.Fatalf("non-deterministic file count: %d vs %d", len(resp.Files), len(resp2.Files))
	}
	for i := range resp.Files {
		if resp.Files[i].Name != resp2.Files[i].Name {
			t.Fatalf("non-deterministic file order: %s vs %s", resp.Files[i].Name, resp2.Files[i].Name)
		}
		if !bytes.Equal(resp.Files[i].Contents, resp2.Files[i].Contents) {
			t.Errorf("non-deterministic contents for %s", resp.Files[i].Name)
		}
	}

	snapDir := filepath.Join(root, "testdata", "snapshots")
	emitted := map[string][]byte{}
	for _, f := range resp.Files {
		emitted[f.Name] = f.Contents
	}

	if *updateSnapshots {
		if err := os.MkdirAll(snapDir, 0o755); err != nil {
			t.Fatalf("create snapshots dir: %v", err)
		}
		for name, contents := range emitted {
			if err := os.WriteFile(filepath.Join(snapDir, name), contents, 0o644); err != nil {
				t.Fatalf("write snapshot %s: %v", name, err)
			}
		}
		// Drop stale snapshots so the directory mirrors the output exactly.
		entries, _ := os.ReadDir(snapDir)
		for _, e := range entries {
			if _, ok := emitted[e.Name()]; !e.IsDir() && !ok {
				os.Remove(filepath.Join(snapDir, e.Name()))
			}
		}
		return
	}

	for name, contents := range emitted {
		snapPath := filepath.Join(snapDir, name)
		want, err := os.ReadFile(snapPath)
		if err != nil {
			t.Errorf("no snapshot for %s (%v) — run: go test ./gen -run TestGolden -update", name, err)
			continue
		}
		if !bytes.Equal(want, contents) {
			t.Errorf("snapshot mismatch for %s (-snapshot +current):\n%s", name,
				cmp.Diff(string(want), string(contents)))
		}
	}
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatalf("read snapshots dir: %v — run: go test ./gen -run TestGolden -update", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := emitted[e.Name()]; !ok {
			t.Errorf("stale snapshot %s no longer emitted — run: go test ./gen -run TestGolden -update", e.Name())
		}
	}
}
