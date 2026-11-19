# codecheck-sandbox: notes for Claude Code sessions

- **Start here:** the "Where we are (resume here)" section at the top of
  [docs/plans/2026-11-19-codecheck-sandbox.md](docs/plans/2026-11-19-codecheck-sandbox.md),
  then the next unticked task in that plan.
- **Design:** [the spec](docs/specs/2026-11-19-codecheck-sandbox-wizard.md),
  including its "Amendments after the spikes"; the spike evidence lives in
  [the spike findings](docs/plans/2026-11-19-codecheck-sandbox-spikes.md).
- **How to work:** follow the plan's "Conventions for every task" and
  [CONTRIBUTING.md](CONTRIBUTING.md): test-first, every external command through
  `system.Runner`, one Conventional Commit per task, `CHANGELOG.md` under
  `## [Unreleased]`, docs updated with behaviour.
- **Versions:** stay pre-1.0 (`0.y.z`) until the maintainer says otherwise.
- **Checks before a commit:** `go test ./... && go vet ./...`, `gofmt -l .`
  (prints nothing) and `npx --yes prettier@3.9.10 --check .`.
- **Public repository:** never commit personal details, private document links,
  real API keys or machine-specific paths. The PostHog `phc_` project key may
  appear, since it only writes events.
