VENV := tools/.venv/bin

.PHONY: fmt-schema fmt-docs check-docs validate

fmt-schema: ## format schema SQL files (sqlfluff; pgb fmt replaces this in v0.2)
	$(VENV)/sqlfluff format testdata/

fmt-docs: ## format fenced code blocks in docs/*.mdx
	python3 tools/fmt-docs.py

check-docs: ## CI gate: docs formatter + link + build checks
	python3 tools/fmt-docs.py --check
	cd docs && npx mint broken-links && npx mint validate

validate: ## regenerate golden output via sqlc@main (parse/analyze acceptance)
	cd testdata/golden && sqlc generate
