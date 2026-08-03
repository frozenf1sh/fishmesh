# FishMesh repository instructions

These rules apply to every change in this repository.

## Read before changing Go code

1. Read `docs/design/code-organization.md` completely.
2. For Serving code, also read `docs/design/serving-domain-redesign.md`.
3. Preserve the dependency direction and file layout defined there. Do not add
   `shared`, `common`, `utils`, or `helpers` packages.

## Change discipline

- Keep behavior changes separate from mechanical package moves.
- Migrate one domain at a time; every migration commit must pass
  `go test -race ./...`, `go vet ./...`, `go build ./...`, and `make manifest`.
- Do not create an interface only to satisfy a naming template. A package
  contract may be an interface or a concrete exported API; interfaces require
  a real substitution boundary.
- Protocol names, routing modes/reasons, environment keys, metric labels, and
  non-obvious limits must not be repeated as string or numeric literals.
- Keep user-owned artifacts and untracked files out of commits.
- Every completed stage updates `docs/stages/`, `docs/stages/README.md`, and
  `docs/notes/project-status.md` before commit and push.
