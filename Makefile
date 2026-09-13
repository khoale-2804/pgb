VENV := tools/.venv/bin

.PHONY: fmt-schema fmt-docs check-docs validate validate-plugin plugin test integration install-plugin

fmt-schema: ## format schema SQL files (sqlfluff; pgb fmt replaces this in v0.2)
	$(VENV)/sqlfluff format testdata/

fmt-docs: ## format fenced code blocks in docs/*.mdx
	python3 tools/fmt-docs.py

check-docs: ## CI gate: docs formatter + link + build checks
	python3 tools/fmt-docs.py --check
	cd docs && npx mint broken-links && npx mint validate

validate: ## regenerate golden output via sqlc@main (parse/analyze acceptance)
	cd testdata/golden && sqlc generate

plugin: ## build the sqlc process plugin binary
	go build -o bin/sqlc-gen-pgb ./cmd/sqlc-gen-pgb

test: ## run all Go tests (integration test skips itself without sqlc)
	go test ./...

integration: ## run the integration suite against docker compose up paradedb
	go test -tags integration ./testdata/integration/

install-plugin: ## install the plugin binary onto PATH (sqlc resolves cmd via PATH)
	go install ./cmd/sqlc-gen-pgb

bin/sqlc-gen-pgb: $(shell find gen core ir cmd -name '*.go')
	go build -o $@ ./cmd/sqlc-gen-pgb

# sqlc resolves every path relative to the config file's directory, and the
# plugin cmd must be an executable (no shell) — so validate-plugin writes a
# config variant next to the fixture with the freshly built absolute binary.
validate-plugin: bin/sqlc-gen-pgb ## e2e: sqlc generate through the plugin
	cd testdata/golden && \
	sed "s|cmd: \"sqlc-gen-pgb\"|cmd: \"$(CURDIR)/bin/sqlc-gen-pgb\"|" \
		sqlc-plugin.yaml > .pgb-plugin-gen.yaml && \
	sqlc generate -f .pgb-plugin-gen.yaml && \
	rm .pgb-plugin-gen.yaml
