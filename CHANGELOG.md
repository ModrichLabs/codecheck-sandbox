# Changelog

All notable changes to this project get recorded in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
the project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every release stays pre-1.0 (`0.y.z`) until the maintainer declares 1.0.0: until
then, a minor bump may break compatibility, and a patch bump only fixes.

## [Unreleased]

### Added

- The `codecheck-sandbox` command tree, with `--version` and a `version`
  command. The lifecycle commands (`setup`, `doctor`, `selftest`, `canary`,
  `review`, `rebuild`, `uninstall` and `telemetry`) exist but report "not
  implemented".
- Global flags `--yes`, `--dry-run`, `--plain`, `--debug` and `--no-telemetry`.
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
