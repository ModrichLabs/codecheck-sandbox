## What and why

<!-- What this changes, and the problem it solves. Link the issue. -->

## Sandbox boundary

<!-- Does this touch the jailer, network rules, proxy, cc-launch, the guest
image, the API key, or what leaves the machine? If so, explain why it stays
safe and update docs/architecture.md. -->

- [ ] No boundary change
- [ ] Boundary change, explained above and in `docs/architecture.md`

## Checklist

- [ ] Tests written first, and they cover the change
- [ ] `go test ./...`, `go vet ./...` and `gofmt -l .` pass
- [ ] `npx --yes prettier@3.9.10 --check .` passes
- [ ] `CHANGELOG.md` updated under `## [Unreleased]` (user-visible changes)
- [ ] Docs updated (`docs/usage/` or `docs/architecture.md`)
- [ ] Commits follow Conventional Commits

## How I tested it

<!-- Commands run, and for integration changes, the distro and kernel. -->
