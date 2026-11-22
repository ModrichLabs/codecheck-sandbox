# Changelog

All notable changes to this project get recorded in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every release stays pre-1.0 (`0.y.z`) until the maintainer declares 1.0.0: until
then, a minor bump may break compatibility, and a patch bump only fixes.

## [Unreleased]

### Added

- The `codecheck-sandbox` command tree, with `--version` and a `version`
  command. The lifecycle commands `doctor`, `rebuild` and `uninstall` exist but
  report "not implemented".
- Global flags `--yes`, `--dry-run`, `--plain`, `--debug` and `--no-telemetry`.
- `review <path|url>` runs a CodeCheck review inside the sandbox, with `--pr`,
  `--base`, `--budget`, and `--cwe` and `--prompt` to focus it. It shows live
  status, then the report, and exits 1 when a review doesn't end cleanly.
- `setup`, the guided wizard: platform checks, packages, host network and guest
  image, each root step shown before it runs, then the selftest. `--yes` runs
  unattended with the key from `CODECHECK_API_KEY`, `--dry-run` changes nothing,
  and `--kernel FILE` supplies the guest kernel if its download fails.
- `selftest`, which boots a VM that tries every way out of the sandbox, and
  `canary`, a small paid review of a planted SQL injection that checks reviews
  work end to end.
- `telemetry on|off|status`, and `config.toml` for the default budget, the
  reports folder, the CodeCheck commit and the telemetry choice. Telemetry stays
  off unless you turn it on.
- Project documentation: README, architecture overview, usage guide, and the
  design spec, plan and spike findings.
- Licence (Apache-2.0), NOTICE crediting CodeCheck, and contribution, security
  and conduct guidelines.

### Security

- Upgrade the indirect dependency `github.com/klauspost/compress` to v1.20.3 for
  CVE-2026-63209. The vulnerable `s2` package never compiled into the binary,
  which uses only `zstd` through `posthog-go`.
- Upgrade the indirect dependency `golang.org/x/crypto` to v0.58.0 for 15
  advisories (among them CVE-2026-39830 to CVE-2026-39835, CVE-2026-42508 and
  CVE-2026-46595 to CVE-2026-46598). All of them concern its SSH and SSH agent
  packages, which the binary never imports.

[Unreleased]: https://github.com/ModrichLabs/codecheck-sandbox/commits/main
