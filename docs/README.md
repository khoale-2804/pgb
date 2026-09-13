# pgb documentation

Mintlify docs site for pgb — two-stage Postgres codegen with full ParadeDB pg_search support.
Theme: aspen. Brand accent is the ParadeDB indigo, set in `docs.json` (`#4f46e5` primary and
light, `#818cf8` dark); aspen needs no CSS color overrides.

## Running locally

```bash
npm install
npx mint dev --port 3736
```

The dev server serves the site at http://localhost:3736.

## Search in local preview

`mint dev` search requires `mint login` with any free Mintlify account — sign in once and
search works in local preview. Search is included in the free Starter tier, so this is not a
paid feature.

## Do not use `mint export`

`mint export` (static self-hosting) is Enterprise-only and loses search. Do not use it to
publish this site. Supported options instead:

- Mintlify free hosting — connect the repo via the GitHub app; custom domains are included.
- The Mintlify OSS program — free Pro for open-source projects.

## Validation

There is no `mint build` command. Validate before committing:

```bash
npx mint broken-links
npx mint validate
```

or both at once: `npx mint broken-links && npx mint validate`.

## Structure

- `docs.json` — theme, colors, appearance, navigation, banner, SEO metatags.
- `welcome/`, `architecture/`, `generated-code/` — pgb's own story: quickstart, schema IR,
  builders and static functions.
- `paradedb/` — pg_search coverage: overview, architecture, index DDL, tokenizers, predicates,
  scoring, aggregations, hybrid search.
- `postgres/` — Postgres version gates (PG15-18 supported, PG19 gated until GA).
- `reference/` — config, directives, type mapping, core API, platform support matrix.
- `guides/`, `future/` — handwritten queries, testing, risks/FAQ, roadmap, plugins, vision.
