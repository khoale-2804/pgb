package gen

import (
	"context"
	"sort"

	"github.com/khoale-2804/pgb/ir"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Generate is the pgb sqlc plugin entry point. Flow: ParseOptions (req
// options JSON) -> Build (catalog -> IR + directives) -> Enrich (oliphant
// re-parse of the schema files: search indexes, COMMENT ON directives,
// generated columns, PK/unique keys, view flags) -> drop pgb:skip tables ->
// PassModels -> PassBuilders -> PassStatics -> sorted response.
//
// Emitted files, deterministic names:
//
//	models.gen.go        pass A (models + enums)
//	<table>.gen.go       pass B (table descriptors + column builders)
//	<table>_repo.gen.go  pass C (filter/set/params types + statics)
//
// Pass A query wrappers (queries.gen.go) are NOT implemented in this slice —
// no such file is emitted. pg_search codegen is M1/M3: Enrich populates
// ir.Table.Search, but no pass consumes it yet, so no search file is ever
// emitted. Every pass returns gofmt'd contents; the file list is sorted by
// name so the response is byte-deterministic for the same catalog + options.
func Generate(ctx context.Context, req *plugin.GenerateRequest) (*plugin.GenerateResponse, error) {
	opts := ParseOptions(req)

	sch, dir, err := Build(req, opts)
	if err != nil {
		return nil, err
	}

	// DDL extraction pass (DESIGN §4): the proto drops CREATE INDEX,
	// COMMENT ON, generated-column kinds and key constraints — re-parse the
	// schema files with oliphant to fill them. Unparseable files degrade to
	// catalog-only (stderr warnings), never fail the run.
	if err := Enrich(req, &sch, &dir, opts); err != nil {
		return nil, err
	}

	// pgb:skip tables emit nothing — no model, no descriptor, no statics.
	// Drop them here so every pass sees the same filtered schema. (PassModels
	// also guards, but the passes B/C receive the schema from here only.)
	tables := make([]ir.Table, 0, len(sch.Tables))
	for _, t := range sch.Tables {
		if t.Skip || dir.Table(t.Schema, t.Name).Skip {
			continue
		}
		tables = append(tables, t)
	}
	sch.Tables = tables

	files := []plugin.File{}

	models, err := PassModels(sch, opts, dir)
	if err != nil {
		return nil, err
	}
	files = append(files, plugin.File{Name: "models.gen.go", Contents: models})

	builders, err := PassBuilders(sch, opts, dir)
	if err != nil {
		return nil, err
	}
	files = append(files, builders...)

	statics, err := PassStatics(sch, opts, dir)
	if err != nil {
		return nil, err
	}
	files = append(files, statics...)

	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	resp := &plugin.GenerateResponse{Files: make([]*plugin.File, 0, len(files))}
	for i := range files {
		resp.Files = append(resp.Files, &files[i])
	}
	return resp, nil
}
