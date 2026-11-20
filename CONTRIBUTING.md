# Contributing to codecheck-sandbox

Thanks for your interest. This project contains a security boundary, so it asks
for a little more care than most: small, well-tested changes, with the reasoning
written down.

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
Report security problems privately, as [SECURITY.md](SECURITY.md) describes,
never in a public issue.

## Before you start

- **Bugs and small fixes:** open an issue first if the fix changes behaviour, so
  we can agree on the cause.
- **Features and design changes:** open an issue to discuss the design before
  writing code. Anything that touches the sandbox boundary (the jailer, the
  network rules, the proxy, `cc-launch`, the guest image, or what leaves the
  machine) needs a short design note and an update to
  [docs/architecture.md](docs/architecture.md).
- Read the [design spec](docs/specs/2026-11-19-codecheck-sandbox-wizard.md) for
  the reasoning behind the current design.

## Development setup

You need Linux, Go 1.27 or newer and git. The integration tests also need KVM,
sudo and an Anthropic API key from a dedicated, spend-limited workspace.

```sh
git clone https://github.com/ModrichLabs/codecheck-sandbox.git
cd codecheck-sandbox
go test ./...
```

## How we work

- **Test first.** Write a failing test, watch it fail for the expected reason,
  then write the least code that makes it pass, then tidy up. Pull requests that
  add behaviour without a test that covers it won't get merged.
- **No direct system calls.** Every external command goes through
  `system.Runner`, so tests can fake it. Commands whose output gets parsed run
  with `LC_ALL=C` and use JSON output where the tool offers it.
- **Secrets stay secret.** Never log, print, or pass on a command line the API
  key or any other secret; route output through the redactor.
- **Checks before every push:**

  ```sh
  go test ./...
  go vet ./...
  gofmt -l .            # must print nothing
  npx --yes prettier@3.9.10 --check .
  ```

  `go test` also runs ShellCheck on the guest scripts in `assets/guest/` when
  `shellcheck` sits on your `PATH`, and skips that check otherwise; install it,
  or run `mise exec shellcheck@0.11.0 -- go test ./assets/`, before changing a
  script.

- **Docs travel with code.** A change in behaviour updates the matching page in
  [docs/usage/](docs/usage/) or [docs/architecture.md](docs/architecture.md) in
  the same pull request.

## Commits, versions and the changelog

- **Commits** follow
  [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`,
  `docs:`, `test:`, `refactor:`, `chore:` and so on, with an optional scope,
  such as `feat(proxy): …`.
- **The changelog** follows [Keep a Changelog](https://keepachangelog.com/). Add
  each user-visible change under `## [Unreleased]` in `CHANGELOG.md`, in the
  same pull request.
- **Versions** follow [Semantic Versioning](https://semver.org/). Every release
  stays pre-1.0 (`0.y.z`) until the maintainer declares 1.0.0. Until then a
  minor bump may break compatibility, and a patch bump only fixes. Only the
  maintainer tags releases.

## Pull requests

1. Branch from `main`; never push to `main` directly.
2. Keep each pull request to one change, with tests and docs.
3. Fill in the pull request template, including how you tested.
4. The maintainer reviews and merges.

## Writing style

Write docs, comments and interface text in Australian English, in plain
language, and prefer active verbs that say what happens ("the proxy refuses the
request") over forms of "to be".

## Licence

This project uses the [Apache License 2.0](LICENSE). By submitting a
contribution, you agree that it falls under that licence, as section 5 of the
licence sets out.
