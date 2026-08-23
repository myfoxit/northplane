## What

<!-- One or two sentences. The commit subject becomes a release-note line — make it read well. -->

## Why

## Checks

- [ ] `make test` and `golangci-lint run ./...` pass (CI also runs `-race`)
- [ ] `cd web && npm run lint && npm test` pass (UI changes)
- [ ] `make types` run and committed if the API changed (`make types-check` is a CI gate)
- [ ] The matching page in `docs/` is updated (`cd docs && npm run build` validates links)
- [ ] e2e specs still assert German UI strings (`make e2e`) for UI changes
