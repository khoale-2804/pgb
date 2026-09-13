// Command sqlc-gen-pgb is the pgb sqlc process plugin. sqlc runs it with a
// GenerateRequest on stdin; it answers with the pgb-generated files (models,
// table descriptors, statics) on stdout. Configure from sqlc.yaml:
//
//	plugins:
//	  - name: pgb
//	    process:
//	      cmd: go run github.com/khoale-2804/pgb/cmd/sqlc-gen-pgb
//	codegen:
//	  - out: db
//	    plugin: pgb
//	    options: { package: db, core: github.com/khoale-2804/pgb/core, target: "18" }
package main

import (
	"context"

	codegen "github.com/sqlc-dev/plugin-sdk-go/codegen"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"

	"github.com/khoale-2804/pgb/gen"
)

func main() {
	codegen.Run(func(ctx context.Context, req *plugin.GenerateRequest) (*plugin.GenerateResponse, error) {
		return gen.Generate(ctx, req)
	})
}
