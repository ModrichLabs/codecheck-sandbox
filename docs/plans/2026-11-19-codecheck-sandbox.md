# Plan: codecheck-sandbox implementation

## Where we are (resume here)

_Updated 2026-11-21._

- **Done:** M0 (spikes S1–S5 and the gate; all spec amendments approved) and M1
  (scaffold, command tree, licence, community files, documentation skeleton).
  `/code-review medium` ran on M1; 7 findings fixed, 2 deferred on purpose (the
  global flags reach no command until M13; stub commands accept any arguments
  until each gets real behaviour).
- **Merged:** M1 (#1), M2 the system layer (#2), M3 the step engine (#3), M3a
  logging and telemetry (#4), M4 platform and distro (#5), M5 pins and downloads
  (#7), M6 host network (#9) and M7 egress proxy (#10) into `main`.
- **In progress:** M8, guest init and assets, on branch `feat/guest-init` in its
  own worktree; the next unticked M8 task comes next. The maintainer chose
  (2026-11-21) to have the host precompute diff-mode changes, so Claude gets no
  Bash at all (spec amendment 9, recorded with the `cc-claude` task).
- **Open question for the maintainer:** the kernel pin points at Firecracker's
  CI bucket, not a release of this repository; mirroring it means publishing a
  GPL-2.0 binary, with its source-offer duties, so that waits for a decision.
- **Dependency pins:** choose versions at least two weeks old;
  `charm.land/log/v2` v2.0.1 and `posthog-go` v1.40.1 so far.
- **Waiting on the maintainer:** nothing until M12's telemetry consent screen,
  unless a task blocks.
- **Machine facts for later milestones:** the spike files live outside the
  repository in `~/spike/` (Firecracker v1.18.0 binaries and the guest kernel in
  `~/spike/fc/`, the Ubuntu squashfs in `~/spike/ubuntu/`, the Claude Code
  native binary under `~/spike/npm/node_modules/`). Root and interactive steps
  (sudo, hidden key prompts) run in the maintainer's own terminal, never through
  a non-interactive shell.
- **Kept for history:** the CodeCheck fork's `feat/codecheck-sandbox` branch;
  don't delete it without asking.

## Context

The approved spec, `docs/specs/2026-11-19-codecheck-sandbox-wizard.md`,
describes `codecheck-sandbox`: a Go CLI with a Bubble Tea interface that sets
up, checks and runs CodeCheck reviews inside jailed Firecracker microVMs, on any
Linux machine with KVM. This plan breaks that spec into small, test-first tasks
that a fresh session can pick up.

Work runs inline, one task at a time: tick each box, commit, and note progress
in this file. Spikes, the spike gate and the first M1 task happened in a
CodeCheck fork (`ModrichLabs/CodeCheck`, branch `feat/codecheck-sandbox`); on
2026-11-20 the project moved to this standalone repository (spec amendment 7).

**Goal:** `codecheck-sandbox setup` takes a Linux machine with KVM from nothing
to a verified sandbox, and `codecheck-sandbox review` runs a contained CodeCheck
review. Every other lifecycle command works too.

**Architecture:** A step engine with `Check`, `Plan`, `Apply` and `Undo` for
each step, sitting on a fakeable `Runner`. Around it:

- A Bubble Tea interface, with a plain mode for scripts and CI.
- A built-in CONNECT proxy that injects the API key.
- A root-owned launch helper, `cc-launch`.
- A builder VM that makes the guest root disk.

The spec, sections 1–9, holds the detail. This plan cites those sections instead
of repeating them.

## Conventions for every task

- **Working folder:** a git worktree of this repository on a feature branch;
  never commit to `main`. Go module: `github.com/ModrichLabs/codecheck-sandbox`
  at the repository root, using Go 1.27.
- **Test-first:** write the test, run it and watch it fail for the expected
  reason. Then write the minimal code, see it pass and refactor. Production code
  written before its test gets deleted and rewritten test-first.
- **Checks after each task:** `go test ./... && go vet ./...`, plus
  `gofmt -l .`, which must print nothing.
- **Commits:** Conventional Commits, one per task, ending with the
  `Co-Authored-By` line. Commits get signed; if signing fails, ask the
  maintainer rather than skip it.
- **Changelog and versions:** each user-visible change gets an entry in
  `CHANGELOG.md` under `## [Unreleased]`. Releases stay pre-1.0 (`0.y.z`) until
  the maintainer declares 1.0.0.
- **Docs:** when a task changes behaviour, update `docs/architecture.md` or
  `docs/usage/` in the same commit.
- **Language:** Australian English and E-Prime in comments, interface text and
  docs.
- **System calls:** never shell out directly. Every command goes through
  `system.Runner`. Parsed commands run with `LC_ALL=C` and use JSON output
  (`ip -j`, `nft -j`).
- **When blocked,** or when a spike contradicts the spec: stop and ask the
  maintainer, and don't improvise around it.

**Libraries:**

- Interface, all on Charm v2 (approved at the spike gate):
  `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/huh/v2`,
  `charm.land/lipgloss/v2`, `charm.land/glamour/v2`, `charmbracelet/log` and
  `charmbracelet/x/exp/teatest/v2`. Check each module's import path against
  `proxy.golang.org` when adding it.
- Command line: `spf13/cobra`.
- Signature checks: `ProtonMail/go-crypto/openpgp`.
- Everything else comes from the standard library.

## M0. Spikes (time-boxed and manual; findings go in `docs/plans/2026-11-19-codecheck-sandbox-spikes.md`)

- [x] **S1 Firecracker basics (2 h).** Done 2026-11-19; see the spikes file.
      Changes: v1.18.0, reboot not power-off, a cgroup wait for `--new-pid-ns`.
  - Install Firecracker v1.17 from its release tarball into `~/spike/fc`.
  - Boot the CI guest kernel with a busybox initramfs as a normal user. Then
    boot it under `jailer --cgroup-version 2` through
    `sudo systemd-run --scope -p RuntimeMaxSec=60`, with the files bind-mounted
    read-only into the chroot.
  - Record the exact jailer flags, whether bind mounts work, and whether
    `poweroff` or `reboot -f` makes Firecracker exit.
- [x] **S2 Ubuntu base (2 h).** Done 2026-11-19; see the spikes file. Proposes a
      Go guest init.
  - Download the Ubuntu 24.04 cloud squashfs and its `SHA256SUMS` and
    `SHA256SUMS.gpg`, and verify the signature with `gpg`.
  - Boot it as the root device, read-only, with `init=/bin/sh`.
  - Record the device names, the drivers needed, and the boot arguments.
- [x] **S3 Claude Code install (2 h).** Done 2026-11-19 on the host; see the
      spikes file. Proposes an offline guest build with the native binary.
      Inside an S2 VM with temporary network access:
  - Run `npm ci --ignore-scripts` from a lockfile for
    `@anthropic-ai/claude-code@2.1.327`, then check that `claude --version`
    works.
  - Record the native optional packages it pulls and the binary's path and hash.
- [x] **S4 TLS interception (2 h).**
  - Write a 60-line Go proxy in `~/spike` that intercepts CONNECT with a
    self-signed CA and logs the `x-api-key` header it sees.
  - Run `claude -p "say hi"` against it with `HTTPS_PROXY` and
    `NODE_EXTRA_CA_CERTS`.
  - Confirm that the proxy sees the header, that a key swap works, and which
    headers carry auth: `x-api-key` and/or `authorization`.
  - **Done 2026-11-19.** Interception and the key swap work; the key travels
    only in `x-api-key`; the bundled Grep works with no `rg`. Found that
    `--bare` removes Grep, Glob, Agent and Skill: proposed dropping it (see the
    spikes file).
- [x] **S5 sudo handover (1 h).**
  - Build a minimal Bubble Tea program that hands the terminal to `sudo -v`
    through `tea.ExecProcess`, then runs `sudo -n true` while the program owns
    the screen.
  - Test under sudo and, if available, sudo-rs.
  - **Done 2026-11-19.** The handover and the background `sudo -n` both work
    under sudo 1.9.17p2; no sudo-rs on this host. No spec change.
- [x] **Gate.** Commit the spikes file
      (`docs: record codecheck-sandbox spike findings`), then report the results
      to the maintainer. If any spike contradicts the spec, propose a spec
      change and wait for approval.
  - **Done 2026-11-19.** The maintainer approved all four spec changes (Go guest
    init, offline guest build, live status without cost, no `--bare`) and Charm
    v2. The spec's "Amendments after the spikes" section lists them; M5, M7, M8,
    M9, M10 and M11 below reflect them.

## M1. Scaffold

- [x] `go mod init github.com/ModrichLabs/codecheck-sandbox`, and add
      `cmd/codecheck-sandbox/main.go`.
  - **Test first:** `cmd/codecheck-sandbox/main_test.go` runs
    `run([]string{"--version"})` and expects `codecheck-sandbox 0.1.0-dev`.
  - Version string in `internal/buildinfo/version.go`.
- [x] Root cobra command in `internal/cli/root.go`:
  - Global flags `--yes`, `--dry-run`, `--plain`, `--debug` and
    `--no-telemetry`.
  - Stub subcommands `setup`, `doctor`, `selftest`, `canary`, `review`,
    `rebuild`, `uninstall`, `telemetry`, `proxy` (hidden) and `version`. Each
    stub returns "not implemented".
  - Tests check the command tree and the help text.
- [x] Project files: `LICENSE` (Apache-2.0 text), `NOTICE` (copyright Modrich
      Labs; credits CodeCheck, MIT, Commonwealth Bank of Australia),
      `CHANGELOG.md` (Keep a Changelog, `## [Unreleased]`, a pre-1.0 note),
      `.gitignore` (`/dist`, `/testdata/**/*.out`, `.claude/worktrees/`) and
      `.editorconfig`.
- [x] Community files: `CONTRIBUTING.md` (Conventional Commits, test-first,
      checks to run, changelog rule, DCO-style sign-off not required),
      `SECURITY.md` (private reporting through GitHub security advisories,
      supported versions `0.x` latest only, scope and threat model in brief),
      `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1), and
      `.github/ISSUE_TEMPLATE/` (bug, feature) plus
      `.github/pull_request_template.md`.
- [x] Documentation skeleton: `README.md` (what it does, status: pre-release,
      requirements, quick start, docs map, licence, CodeCheck credit),
      `docs/architecture.md` (components, trust boundaries, review flow, key
      handling and telemetry, from spec §§1–6 and §11), and `docs/usage/` with
      `README.md` (index), `requirements.md`, `install.md`, `setup.md`,
      `commands.md`, `troubleshooting.md`, `privacy-and-telemetry.md` and
      `uninstall.md`. Pages for features not built yet say so ("planned")
      instead of describing behaviour.
- [x] Markdown formatting: `npx --yes prettier@3.9.10 --check .` passes, with a
      `.prettierrc` (`proseWrap: always`).
- [x] Open the bootstrap PR after asking the maintainer (opened 2026-11-20).

## M2. System layer (`internal/system`)

- [x] `runner.go`:
  - `type Cmd struct{ Name string; Args []string; Stdin []byte; Env []string; Root bool }`,
    `type Result struct{ Stdout, Stderr []byte; Code int }`, and
    `type Runner interface{ Run(ctx, Cmd) (Result, error) }`.
  - `ExecRunner` sets `LC_ALL=C` and never runs a shell.
  - **Tests:** argument passing, `LC_ALL`, exit codes, and stdin set to
    `/dev/null` when empty.
  - _Done:_ a non-zero exit fills `Result.Code` without an error; `Run` errors
    only when the command fails to start or the context ends. `ExecRunner`
    refuses `Root:true` with `ErrRootNeeded`, leaving that to `privilege.go`.
- [x] `privilege.go`:
  - Detect `sudo`, sudo-rs, `doas` and `run0` from `PATH`, plus `sudo --version`
    output.
  - `Cmd{Root:true}` runs through `<tool> -n`.
  - `WriteRootFile(path, bytes, mode)` writes through `sudo -n tee` and then
    `chmod`, from in-memory bytes.
  - **Tests** use a fake runner and assert the exact argv for each tool.
  - _Done:_ `WriteRootFile` runs `install -m <mode> -T /dev/null <path>` and
    then `dd of=<path> status=none`, so a secret never sits under looser
    permissions or echoes back as stdout (a change from `tee` then `chmod`;
    later tasks that say `sudo tee` mean `WriteRootFile`). Root commands run as
    `<tool> -n -- <cmd>`; run0 gets `--no-ask-password --setenv=LC_ALL=C`
    instead of `-n`. `PrivRunner` refuses `Env` on root commands, which the
    tools would drop; doas drops `LC_ALL=C` too, so parsed root output should
    stick to JSON.
- [x] `fake.go`: `FakeRunner` records calls and replays scripted results, keyed
      by an argv prefix. It panics on any unexpected call, so tests fail loudly.
  - _Done:_ `On(prefix...).Return(res)` or `.Fail(err)`; each script answers
    once, in the order added; `Calls()` and `Pending()` support assertions.
- [x] `fs.go`: an `FS` interface (read, write, stat, mkdir, remove) with an
      `OSFS` and a `MemFS` for tests.
  - _Done:_ one contract test runs against both; errors match the `os`
    package's, so `errors.Is(err, fs.ErrNotExist)` works on either.
- [x] `keepalive.go`: a goroutine that runs `sudo -n -v` every 60 seconds until
      the context ends, and reports the first failure on a channel. Tested with
      a fake clock.
  - _Done:_ `KeepAlive` takes the tick channel (the fake clock);
    `StartKeepAlive` wires a real 60-second ticker. doas runs `-n true`, which
    detects expiry but cannot extend it; run0 runs nothing. A non-zero exit
    reports `ErrLoginExpired` with the tool's stderr.
- [x] `redact.go`: `Redactor` replaces registered secrets in any byte stream.
  - **Test:** a key registered once never appears in the output of the log
    writer or the runner's error messages.
  - _Done:_ `Redactor.Writer` catches a secret split across writes, holding back
    only a tail that could start one; `Redactor.Runner` redacts stderr and error
    messages (keeping the error chain) but leaves stdout alone.
- [x] Commit each file pair separately (`feat(system): …`).

## M3. Step engine (`internal/step`)

- [x] `step.go`:
  - `type Step interface{ ID() string; Title() string; Phase() Phase; Check(ctx) (Status, error); Plan(ctx) (Plan, error); Apply(ctx) error; Undo(ctx) error }`.
  - `Plan` holds `[]PlannedCmd` and `[]PlannedFile{Path, Mode, Content}`, plus a
    `NeedsRoot` flag.
- [x] `engine.go`:
  - `Run(ctx, steps, Mode{DryRun, AssumeYes}, events chan<- Event, confirm ConfirmFunc)`.
  - It runs `Check`, and skips a step when the check passes. Otherwise it builds
    the plan, asks for confirmation (unless `--yes`), applies, and checks again.
  - Events: `StepStarted`, `StepPlanned`, `StepNeedsConfirm`, `StepDone`,
    `StepFailed`, `NeedsSudo` and `Progress`.
  - **Tests:** skip-when-passing; dry-run never calls `Apply`; a declined
    confirm stops the run; a failed re-check marks the step failed; event order.
  - _Done:_
    `Run(ctx, steps, Options{DryRun, AssumeYes, Events, Confirm, Elevate})`; the
    `Elevate` hook runs the `sudo -v` handover once, before the first root step
    that applies, and again (with one retry) when `Apply` returns
    `system.ErrLoginExpired`. The first failure or a refusal stops the run.
    Steps report progress with `ReportProgress(ctx, msg, fraction)`.
- [x] `undo.go`:
  - `Uninstall(ctx, steps, manifest)` runs `Undo` in reverse, only for steps the
    manifest lists as `created`.
  - **Tests:** order, and the skipping of `adopted` and missing entries.
  - _Done:_ `Uninstall(ctx, steps, manifest, Options)`. `Step.Undo(ctx, items)`
    receives only the step's created items, so a step that adopted some packages
    and created others removes only the latter. It elevates once before the
    first `Undo` and again on `system.ErrLoginExpired`; a dry run changes
    nothing. A failed `Undo` doesn't stop the rest, and `Uninstall` returns
    every failure joined. A nil manifest gives `ErrNoManifest`.
- [x] `manifest.go`:
  - Schema
    `{version:1, installedBy, items:[{id, kind, path, state:created|adopted}]}`.
  - Load and save through `system.WriteRootFile` to
    `/var/lib/codecheck-sandbox/manifest.json`.
  - Every path gets checked against a compiled allowlist (spec §8).
  - **Tests:** round trip; rejection of a path outside the allowlist; an unknown
    schema version gets refused.
  - _Done:_ items gain an optional `name` (for users, packages, units and
    network items, which have no path); file and folder items need an allowed
    path, others may not carry one. Paths must be absolute and already clean.
    Names must come from a code-defined list (`allowedNames`: the jail user, the
    units, `cc-tap0`, `inet codecheck`, and spec §3's packages under each distro
    family's name; M6 must keep the package list in step). Unknown fields,
    kinds, states and trailing data get refused. `Save` fills
    `manifest.json.new` and renames it over the manifest with `mv -f -T`.
- [x] `lock.go`: `flock` on `/run/codecheck-sandbox/lock` with a timeout.
      **Test:** two lockers in one process, where the second gets `ErrBusy`.
  - _Done:_ `Lock(ctx, path, timeout)` polls a non-blocking `flock`, stops when
    `ctx` ends, and opens the file read-only without ever creating it. **Later
    (M9):** a `tmpfiles.d` entry must create `/run/codecheck-sandbox/`
    (root, 0755) and the `lock` file in it (root, 0644) at boot. A file the user
    created would stop root's `cc-launch` from opening it under
    `fs.protected_regular`.

## M3a. Logging and telemetry (`internal/logging`, `internal/telemetry`; spec §11)

- [x] `logging/logging.go`: a `slog` JSON handler that writes through
      `system.Redactor` to a rotating file (10 MB, five kept), plus an optional
      `charmbracelet/log` tee for `--debug`. **Tests:** a registered secret
      never appears in the file; rotation keeps five files; the level follows
      `--debug`.
  - _Done:_ `New(Options{Dir, Redactor, Debug, Console, MaxSize})`. Five files
    in all (`codecheck-sandbox.log` plus `.1` to `.4`), each 0600 in a 0700
    folder; rotation uses the standard library. The `--debug` console copy
    (`charm.land/log/v2`) goes through the Redactor too.
- [x] `telemetry/events.go`: typed event structs for usage, errors, environment
      profile and security signals, with enum and band types (`DurationBand`,
      `CostBand`, `SizeBand`). **Tests:** band edges.
  - _Done:_ every field holds a `bool` or a package type with an unexported
    `value()` method that validates itself on encoding (unknown enum or band →
    `"other"`, a bad version → `"invalid"`); `Properties` refuses any other
    field type. Also `CountBand` (sub-agents, proxy denials). Step IDs count
    only once registered: **later step milestones (M6–M11) must call
    `telemetry.RegisterStepIDs` from `init`.**
- [x] `telemetry/allowlist_test.go`, the safety test: fill every event with the
      worst values each type allows, add a planted key, path, repo URL, hostname
      and username to the surrounding context (step errors, config,
      environment), serialise, and assert that none of them appear. A reflection
      walk also fails the test if any event field has a plain `string` type
      outside the enum and version types.
  - _Done:_ the test plants an IP address and an error message too. It caught
    two leaks through `Version` on its first run, both fixed: an IPv4 address
    passed as a four-part version, and a free-text suffix (`1.0-alice`) passed
    as a pre-release. Versions now allow three numeric parts and only `dev`,
    `rc`, `alpha` and `beta` suffixes.
- [x] `telemetry/exception.go`: build `$exception` properties from an error: the
      kind from `errors.As` against known types or sentinels, never
      `err.Error()`; a stack limited to frames inside the module, with file
      paths cut to the module root. **Tests:** an error wrapping
      `/home/x/secret` yields neither the path nor the message.
  - _Done:_ `NewException(err, stepID)`; `kindRules` lists the project's
    sentinels, then context, filesystem and exec errors; the rest become
    `unknown`. File paths come from the function's package plus the file's base
    name, never the build path. Frame fields have their own validating types,
    and the allowlist test covers them. Later milestones that add sentinels
    should add them to `kindRules`. **Stacks** come only from an error in the
    chain with a `Callers() []uintptr` method (recorded where it arose); without
    one, the exception sends no stack, since the reporting site's frames would
    mislead. A later milestone may add such an error type in a small package
    that `cc-launch` can import too. Package paths may contain `-`
    (`cmd/codecheck-sandbox`). `EnvironmentProfile.Kernel` uses `KernelVersion`,
    which sends major.minor only.
- [x] `telemetry/consent.go`: resolve on/off from `DO_NOT_TRACK`,
      `--no-telemetry`, `--yes`, TTY and `config.toml`; create or delete the
      install id. **Tests:** a table of every combination.
  - _Done:_ `Resolve(ConsentInputs)` returns `On`, `Off` or `Ask` (no stored
    choice, a TTY and no `--yes`; only the setup wizard asks, and other commands
    treat it as `Off`). The config arrives as a `*bool`, so M13's `config.go`
    supplies it. `EnsureInstallID` keeps a UUIDv4 in `telemetry/install-id`
    (0600) and replaces a tampered file; `Forget` deletes the id and the queue.
- [x] `telemetry/client.go`: a `Client` interface (`Capture`, `Flush`) with
      `Noop` and `PostHog`. Queue to
      `~/.local/state/codecheck-sandbox/telemetry/queue.jsonl` (1 MB cap, oldest
      dropped); `Flush` sends through `posthog-go` (pinned version, feature
      flags off, no debug images) with a 3-second deadline, then clears the
      queue; failures go only to the local log. Every event gets
      `$process_person_profile: false` and `$geoip_disable: true`. **Tests:** an
      `httptest` server receives the batch with the expected properties; a
      hanging server never delays `Flush` past the deadline; a server error
      leaves the command's exit code alone.
  - _Done:_ `New(Options)`; `Flush` takes the queue first (at-most-once: a
    failed send drops events). posthog-go adds `$os`, `$os_version`,
    `$os_distro` and `$go_version` after its `BeforeSend` hook, so a wrapping
    HTTP transport (`stripTransport`) filters every batch against the property
    allowlist as the last step before the network. It refuses anything other
    than a POST to `/batch/`, and any body it cannot decode. No `SecretKey` (no
    flag polling), no retries, GeoIP off. If the queue can't be deleted, `Flush`
    sends nothing. **Accepted risk:** the queue has no lock, so two CLI commands
    running at once for one user could lose an event; the proxy runs as a
    separate `DynamicUser` and never shares this queue.
- [x] Hook points: the engine emits `step_finished` through an injected client
      (M3 tests use `Noop`); `cli` wraps every command for `command_run` and a
      final `Flush`. `cc-launch` never imports `internal/telemetry`; an import
      test enforces that.
  - _Done:_ `step` can't import `telemetry` (telemetry imports step for its
    error kinds), so `step.Options.OnStepFinished` takes a `StepResult` and
    `telemetry.StepRecorder(client)` adapts it.
    `cli.NewRootCommandWith(factory)` wraps every subcommand. **For M13:**
    `defaultTelemetry` returns `Noop` until `config.go` can supply the stored
    consent; M13 must wire `telemetry.Resolve`, `EnsureInstallID`, the state
    folder and `telemetry.New` there, and open the log with `logging.New`. The
    import test checks every `cmd/cc-*` package for `internal/telemetry`,
    `posthog-go` and `net/http`; checked against a throwaway `cmd/cc-probe`, it
    caught all three.
- [x] Pins: add the PostHog host `https://us.i.posthog.com` and the project key
      to `internal/pins`. Project key (public, write-only; a dedicated
      organisation in US Cloud, confirmed 2026-11-19 by a 200 from
      `us.i.posthog.com/flags` and a 401 from EU):
      `phc_pR2TVwdPiaMaERt5bD6h77nvDkhq59G7bsvip8aPNyiP`. An empty key still
      makes `PostHog` fall back to `Noop` (keep that test). Builds may override
      it with `-ldflags -X` so forks can point at their own project.
- [x] Commit each file pair separately (`feat(telemetry): …`,
      `feat(logging): …`).

## M4. Platform and distro (`internal/platform`, `internal/distro`)

- [x] `distro/osrelease.go`: parse `/etc/os-release`, then map `ID` and
      `ID_LIKE` to a family: arch, debian, fedora, suse or unknown. Table-driven
      tests use real samples in
      `testdata/os-release/{arch,omarchy,ubuntu-24.04,debian-12,fedora-41,rhel-9,opensuse-tumbleweed,alpine}`.
  - _Done:_ the SUSE family takes the name `opensuse`, matching the telemetry
    enum. `Detect` falls back to `/usr/lib/os-release`. The `omarchy` sample
    comes from the maintainer's machine; the others follow each release's
    published os-release, written from reference rather than copied from a
    running system.
- [x] `distro/packages.go`: map the logical packages `nftables`, `e2fsprogs`,
      `squashfs-tools`, `git`, `iproute2` and `gnupg` to package names and an
      install argv for each family. Golden test of every family's argv.
  - _Done:_ `InstallCommands` returns a list of argvs, since Debian runs
    `apt-get update` first; it sets `DEBIAN_FRONTEND` through `env` because root
    commands carry no `Env`. Arch uses `pacman -S --needed` without `-y`, to
    avoid a partial upgrade; the review suggested `-Syu`, but setup should never
    upgrade the whole system on its own (least of all under `--yes`), so
    `requirements.md` asks Arch users to upgrade first instead. An empty package
    list gives no commands. An unknown family returns `ErrUnknownFamily` naming
    every package. A test checks every installable name against the manifest's
    package allowlist. Golden files: `go test ./internal/distro -update`
    rewrites them.
- [x] `platform/checks.go`. Each check below becomes a `Step` in phase Platform,
      so it reports through the engine; checks that only read have `Apply`
      return "fix manually: …".
  - **CPU architecture:** x86_64 only; aarch64 gets a clear "deferred" message.
  - **KVM access for `fcjail`:** `/dev/kvm` exists, and its owner, group and
    mode let that user open it.
  - **systemd** and **cgroups v2**: from `/sys/fs/cgroup/cgroup.controllers`.
  - **RAM:** at least 8 GB, from `/proc/meminfo`.
  - **Free disk:** at least 10 GB on `/var/lib`.
  - **Firewall managers:** firewalld, ufw and Docker detected.
  - **Security module:** SELinux or AppArmor, and its mode.

  **Tests:** each check against `MemFS` fixtures.

  - _Done:_ nine checks with IDs `platform-*`, registered with telemetry. An
    `Env` injects the filesystem, a `Stat` with owner and mode (which `MemFS`
    cannot model), free space, the architecture and `LookPath`; `DefaultEnv`
    reads the real host. `fcjail` doesn't exist yet in this phase, so the KVM
    check needs a character device open to all, or to group `kvm` with read and
    write. RAM accepts 7.5 GiB `MemTotal` and up, since "8 GB" machines report
    less. Added a privilege-tool check from spec §7. `Inspect` returns the
    firewall managers and security module for host setup and telemetry; on the
    maintainer's machine it found ufw and Docker, and every check passed. A
    failing read-only check returns `ErrManual` with a fix-it hint from `Plan`,
    so the engine fails it before any confirm prompt and a dry run reports it.
    The KVM group resolves through `getent` (NSS, so Silverblue's
    `/usr/lib/group`, userdb and LDAP all count). Free disk uses `Bfree`, since
    root writes to `/var/lib`. AppArmor's mode comes from its profile list:
    enforcing, complain, or `enabled` when unreadable (a new telemetry enum
    value). A missing os-release `ID` defaults to `linux`.

## M5. Pins and downloads (`internal/pins`, `internal/fetch`)

- [x] `pins/pins.go`, filled from the spike results:
  - Firecracker and jailer tarball URL and SHA-256.
  - Ubuntu squashfs URL, the `SHA256SUMS` URLs and the signing-key fingerprint.
  - Guest kernel URL (a release of this repository) and its SHA-256.
  - Claude Code version 2.1.327, the `claude-code-linux-x64` tarball URL, its
    npm `sha512` integrity, and the `claude` binary's SHA-256 (spikes file, S3).
  - CodeCheck: repository
    `https://github.com/Commonwealth-Bank-of-Australia/CodeCheck` and commit
    `3231a0def630ff7f1529de47af2ea763d23b8217` (tag `v0.0.1`).

  **Test:** no pin stays empty, every SHA-256 has 64 hex characters, the
  CodeCheck commit has 40 hex characters, and the integrity string starts with
  `sha512-`.

  - _Done:_ every pin got checked against its source on 2026-11-20: Firecracker
    against the release's `.sha256.txt`; the kernel by hashing the bytes the CI
    bucket serves; Ubuntu by matching the dated `release-20261017` `SHA256SUMS`
    to the spike's copy and verifying its signature with the key; the key by its
    fingerprint; CodeCheck's tag with `git ls-remote`; Claude Code's integrity
    against the npm registry. The Ubuntu pin uses the dated folder, since
    `release/` moves to each new build. The kernel URL stays upstream for now
    (see the open question above). Key file:
    `assets/keys/ubuntu-cloudimage.asc`, from `keyserver.ubuntu.com`. A
    reflection test checks every pin's format and that the pins agree.

- [x] `fetch/codecheck.go`: fetch the pinned CodeCheck commit into
      `~/.cache/codecheck-sandbox/codecheck.git` (a bare repository, HTTPS only,
      `git fetch --depth 1 <url> <commit>`, with `core.hooksPath=/dev/null` and
      `protocol.file.allow=never`), check that `rev-parse FETCH_HEAD` equals the
      pin, then `git archive <commit> .claude-plugin skills     LICENSE.txt`
      into the build inputs. **Tests:** against a local bare repository: the
      right commit, a mismatched commit refused, a non-HTTPS URL refused (except
      `file://` in tests through an injected option).
  - _Done:_ `CodeCheck{...}.Archive(ctx)` returns the tar bytes. Beyond the
    plan's flags, every git call also runs with `protocol.allow=never` plus
    `protocol.https.allow=always`, and with `GIT_CONFIG_GLOBAL=/dev/null`,
    `GIT_CONFIG_NOSYSTEM=1` and `GIT_TERMINAL_PROMPT=0`, so a `url.insteadOf` in
    the user's config can't redirect the fetch. Git also runs with the new
    `system.Cmd.CleanEnv`, which passes on only `PATH`, `HOME`, proxy and CA
    variables: `GIT_CONFIG_*` and `GIT_SSL_NO_VERIFY` (which acts even when
    empty) never reach it. A test with real git proved an inherited
    `GIT_CONFIG_COUNT` did redirect the fetch before that fix. The commit must
    be exactly 40 hex characters. The happy-path test fetches by commit from a
    real local bare repository; a `FakeRunner` covers the `FETCH_HEAD` mismatch.

- [x] `fetch/download.go`: HTTPS only, written to a temp file, then the SHA-256
      checked and the file renamed. It reports `Progress` events, and removes
      the file on a hash mismatch.
  - **Tests:** an `httptest` TLS server covers a good hash, a bad hash, plain
    `http` refused and a partial download.
  - _Done:_ `Downloader{Client, Progress}.Download(ctx, url, sha256, dest)`.
    Redirects must stay on HTTPS too (GitHub releases redirect to another host);
    a test covers a redirect to plain `http`. Progress goes to
    `step.ReportProgress` unless injected, at most once per MiB. Any failure
    (hash, HTTP status, a short body) removes the temporary file and leaves
    `dest` untouched. Requests ask for `Accept-Encoding: identity`, so a
    server's `Content-Encoding: gzip` can't change the bytes that get hashed;
    `MaxBytes` (default 4 GiB) caps each download.
- [x] `fetch/gpg.go`: verify `SHA256SUMS` against `SHA256SUMS.gpg` with the
      embedded Ubuntu key in `assets/keys/ubuntu-cloudimage.asc`, then look up
      the squashfs line.
  - **Tests:** a test key signs fixtures; check good, tampered and wrong-key
    cases.
  - _Done:_ `ProtonMail/go-crypto` v1.5.2 (2026-09-25). The key loads through a
    root `assets` package (`go:embed` can't reach parent folders). `VerifySums`
    needs exactly one key whose fingerprint equals the pin. `VerifiedUbuntuSum`
    also requires the signed hash to equal the pinned one. Ubuntu's real
    `SHA256SUMS` and signature for the pinned release sit in
    `internal/fetch/testdata/ubuntu/`; that test found Ubuntu armours its `.gpg`
    signature, so both armoured and binary signatures verify.
- [x] `fetch/npmtar.go`: download the pinned tarball, check its `sha512`
      integrity, extract only `package/claude` (refusing links, absolute paths
      and `..`), cap the size, and check the binary's SHA-256. Never runs npm,
      Node or package scripts.
  - **Tests:** a fixture tarball covers a good file, a bad integrity, a bad
    binary hash, a path-traversal entry and a symlink entry.
  - _Done:_ `ClaudeCodeBinary(d, workDir).Fetch(ctx, dest)`, built on
    `NpmBinary` and a new `Downloader.DownloadSRI` (the download core now takes
    any hash and check). Any unsafe entry, or the member appearing twice or not
    at all, refuses the whole tarball; only files and folders count as safe. Cap
    512 MiB. A one-off smoke test (since removed) fetched the real Firecracker
    release and the real Claude Code tarball through this code: both verified,
    and the 372.9 MB binary matched its pin.

## M6. Host network (`internal/hostnet`)

- [x] Templates in `assets/host/`:
  - `codecheck-net.service`: creates the tap, sets its address, turns IPv6 off
    and brings it up, then loads the rules.
  - `rules.nft`: uses `destroy table`, then `table`, with the review allow rule
    and final drops (spec §3).
  - `nm-unmanaged.conf`: tells NetworkManager to leave the taps alone.

  `hostnet.Render(Config)` fills them in. **Golden tests** in
  `testdata/golden/`.

  - _Done:_ `rules.nft` replaces the table with `table` / `delete table` /
    `table { … }` in one file instead of `destroy table`, which needs nftables
    1.0.8+ (Debian 12 ships 1.0.6); one `nft -f` keeps it atomic. The unit loads
    the rules **before** it creates the tap, so the tap never exists unfiltered
    (the rules match `iifname` strings), clears a stale tap first, and removes
    both on stop. Input hook: `cc-tap0` may reach only `172.16.0.1:3180/tcp`;
    everything else from `cc-tap*` drops. Forward hook: all `cc-tap*` traffic
    drops. `Config.Validate` refuses values that could inject template text or
    widen the rules. The rendered rules loaded cleanly, twice, with real nft
    1.1.7 in an unprivileged namespace (`unshare -rn`), leaving one table; its
    JSON listing became the rule-hash fixture. **For spec §7:** the spec's
    "destroy table" wording now reads as this idiom.

- [x] `hostnet/rulehash.go`: canonical JSON from
      `nft -j list table inet codecheck`, with handles stripped and keys sorted,
      then SHA-256.
  - **Tests:** a fixture JSON gives a stable hash; reordered handles give the
    same hash; an extra rule changes it.
  - _Done:_ `HashRules(json)` and `LiveRuleHash(ctx, runner, table)` (runs
    `nft -j list` as root). The `metainfo` object (nft's version) drops out too;
    rule order stays, and a test shows swapping the accept and drop changes the
    hash. The fixture comes from real nft 1.1.7. **For `RulesStep`:** nft's JSON
    layout may differ between versions, so record the expected hash from the
    live table just after loading, on the same host, never from a precomputed
    value.
- [x] Steps, each with full `Check`, `Plan`, `Apply` and `Undo`, tested with
      `FakeRunner`:
  - `JailUserStep` (`fcjail`, recorded as created or adopted)
  - `JailerBaseStep` (`/srv/jailer`, mode 711)
  - `TapServiceStep`
  - `RulesStep`, which also records the expected hash in
    `/var/lib/codecheck-sandbox/rules.sha256`
  - `NMUnmanagedStep`, only when NetworkManager runs
  - `FirewalldStep` and `UfwStep`, only when they're active: they allow 3180 on
    the tap
  - `SELinuxStep`, only when SELinux enforces
  - _Done:_ `hostnet.Steps(Deps{Config, Runner, FS, Record})` validates the
    config and returns eight steps (`host-*` IDs, registered with telemetry) in
    this order: jail user, jailer base, rules, NetworkManager, tap service,
    firewalld, ufw, SELinux. Rules load before the tap service (which loads them
    again) and NetworkManager learns to ignore the tap before it exists. `Apply`
    records only what it creates, through `Record`, so pre-existing users and
    folders never get removed; M13 collects the records into the manifest.
    Decisions:
    - `fcjail`:
      `useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /bin/false`;
      no `kvm` group, since the jailer creates `/dev/kvm` inside each chroot for
      the jail user.
    - Rules: create `/etc/codecheck-sandbox` and `/var/lib/codecheck-sandbox`
      (0755), write and load the rules, then record the live table's hash. Undo
      deletes the table, the files, then empty folders.
    - firewalld: a dedicated `codecheck` zone holding the tap
      (`--change-interface`, which also moves it out of a zone NetworkManager
      chose) with port 3180/tcp; undo deletes the zone.
    - ufw: `ufw allow in on cc-tap0 to 172.16.0.1 port 3180 proto tcp`, checked
      with `ufw show added`.
    - SELinux: `restorecon -RF` on our paths, checked with a dry run. **Not yet
      tried on an SELinux host; check on Fedora in M14.**
    - Jailer base undo uses `rm -rf --one-file-system`, so it never follows a
      bind mount a crashed run left.
  - _Also:_ `PrivRunner` now turns each privilege tool's "password required"
    reply into `ErrLoginExpired`, so a `Check` that needs root (the live rule
    hash) hands over on the first run instead of failing; sudo's and run0's
    wording checked on this host, sudo-rs's and doas's from their sources. The
    manifest allowlist gains `firewalld zone codecheck` and
    `ufw allow cc-tap0 3180`.
  - _Review:_ `/code-review medium` found 10 issues, all fixed test-first. The
    login match now needs the tool's own prefix (`sudo:`, `doas:`, run0's line),
    so a root command's own "authorization required" stays its error. The rules
    undo treats a table the tap service already removed as gone; the unit's IPv6
    `sysctl` carries `-` for `ipv6.disable=1` hosts; `Steps` de-duplicates
    records, since the engine retries `Apply` after a handover; the tap check
    also needs `ip link show dev cc-tap0`; firewalld checks its zone query,
    plans `--new-zone` only when needed (asking without root, since the engine
    elevates after planning) and skips `--reload` when stopped; the SELinux and
    ufw checks report failed commands; `Validate` refuses the subnet's network
    and broadcast addresses. `systemd-analyze verify` accepts the rendered unit.

## M7. Egress proxy (`internal/proxy`)

- [x] `ca.go`: generate the sandbox CA (ECDSA P-256, 10 years, name constraints
      limited to `api.anthropic.com`), then write the key and certificate
      through `WriteRootFile` to `/etc/codecheck-sandbox/ca/`.
  - **Tests:** the constraints work, and a leaf for another host fails
    verification.
  - _Done:_ besides the DNS constraint, the CA excludes every IP range and
    allows no intermediate CAs; a test showed a rogue leaf for a raw IP verified
    without the exclusion. `LoadCA` refuses a mismatched key or a lost
    constraint. Leaves live a day, in memory only (`LeafCache` renews them). The
    writing moved to `CAStep` (below).
- [x] `proxy.go`, an `http.Handler`:
  - Only `CONNECT` gets through, and only to `api.anthropic.com:443` and from
    the configured client address; anything else gets 403, logged.
  - Inside the tunnel, `POST /v1/messages`, `GET /api/claude_code/settings` and
    `GET /api/claude_code/policy_limits` pass (spike S4); log any other path.
  - The proxy terminates TLS with a leaf certificate from the CA.
  - It rejects any request whose auth header differs from the guest placeholder
    `sk-ant-sandbox-placeholder`.
  - It replaces that placeholder with the real key, then forwards over real TLS.
  - Caps on requests and bytes per connection.

  **Tests**, against an `httptest` TLS upstream:
  - allowed host
  - denied host
  - denied source address
  - plain GET refused
  - wrong key rejected
  - placeholder swapped
  - real key never written to the log
  - caps enforced

  - _Done:_ other paths get 403 and a log line (an allowlist), so the canary
    will show anything Claude Code needs beyond the three. Requests need exactly
    one `x-api-key`, equal to the placeholder, and no `Authorization` header.
    Upstream gets no forwarding headers. Caps per tunnel default to 5,000
    requests and 2 GiB, since keep-alive lets a whole review share one tunnel;
    `--max-budget-usd` bounds the spend. A real-binary smoke test on loopback
    refused a forbidden host and an attacker's key and never logged the key.

- [x] `denylog.go`: a JSON-lines denial log under
      `/var/log/codecheck-sandbox/proxy.jsonl`, with rotation at 10 MB.
      **Test:** the format, and rotation.
  - _Done:_ no header values, long fields cut, mode 0600 through a new
    `logging.OpenRotating`. **For M11/M12:** with `DynamicUser=yes`, systemd
    places `LogsDirectory` under `/var/log/private` (root only), so the post-run
    checks read the denial log as root.
- [x] `cli/proxy.go`: the hidden `proxy` subcommand.
  - Flags `--listen` and `--client`. No build mode: the guest build runs with no
    network (spike gate).
  - Reads the key from `$CREDENTIALS_DIRECTORY/api-key`.

  **Tests** cover flag validation and a missing or empty credential.

  - _Done:_ also `--ca-cert` and `--deny-log`; reads the CA key from
    `$CREDENTIALS_DIRECTORY/ca-key`. `--listen` must name one IPv4 address,
    never a wildcard. A missing, empty or placeholder key gets refused without
    the value appearing in the error. Shuts down on SIGTERM.

- [x] `assets/host/codecheck-proxy.service`: `DynamicUser=yes`,
      `LoadCredential=api-key:/etc/codecheck-sandbox/api-key`,
      `ProtectSystem=strict`, `PrivateTmp`, `NoNewPrivileges`,
      `RestrictAddressFamilies=AF_INET AF_INET6`, `After=codecheck-net.service`,
      `BindsTo=codecheck-net.service`. Golden test. Then `ProxyServiceStep` and
      `ApiKeyStep`.
  - _Done:_ the unit also carries `LoadCredential=ca-key:…` (a dynamic user
    can't read a root-only key), `LogsDirectory=codecheck-sandbox`, an empty
    `CapabilityBoundingSet`, `PrivateDevices`, the `Protect*` family,
    `RestrictNamespaces`, `MemoryDenyWriteExecute` and
    `SystemCallArchitectures=native`; `systemd-analyze verify` accepts it. Four
    host steps join `hostnet.Steps` after ufw: `host-proxy-binary` (installs a
    root-owned copy of the running CLI to
    `/usr/local/lib/codecheck-sandbox/bin/`, checked by SHA-256),
    `host-proxy-ca` (generates the CA once, so the plan shows the very
    certificate written; the key shows masked), `host-api-key` (written 0600,
    masked in the plan; refuses anything not shaped like `sk-ant-…`), and
    `host-proxy-service`. `Config.Validate` now also checks every path against
    the manifest allowlist. **For M10:** a new CA means a guest rebuild, since
    the guest trusts its certificate.
  - _Review:_ `/code-review medium` found 8 issues; 7 fixed test-first. When the
    binary, CA or key step applies, `host-proxy-service` restarts the service,
    so the proxy never keeps old code or secrets. A key the wizard supplies
    always gets written, so re-running setup replaces a revoked key. The CA
    check reads the key as root and loads the pair. On enforcing SELinux, the
    binary and unit files get relabelled as they land. A limit of 16 tunnels at
    once joins the per-tunnel caps (which bound a tunnel, not a run). Requests
    the guest cancels log nothing. The denial log stays 0600, since only root
    can reach it anyway. **Pushed back:** no changelog entries yet, since
    nothing a user runs reaches the proxy until M12/M13 wire setup up.

## M8. Guest init and assets (`cmd/cc-guestinit`, `internal/guestinit`, `assets/guest/`)

- [x] `internal/guestinit/config.go`: parse the fixed 4 KB key=value block from
      the raw config device (mode `build`, `review` or `selftest`, plus run
      settings). **Tests:** valid blocks, unknown keys, oversize values, missing
      mode.
  - _Done:_ keys `mode`, `run_id`, `max_budget_usd`, `scope` (`full` or `diff`),
    `base` (a full 40- or 64-hex commit, diff only) and `probe_ports` (selftest
    only). The parser fails closed: exactly 4096 bytes, every line
    newline-terminated, nothing but NULs after the text, values of 128 bytes at
    most, no repeated or unknown keys, and no key outside its mode. `Encode`
    writes the same block for the host. **For M9/M11:** build the config device
    with `guestinit.Encode`.
- [x] `internal/guestinit/review.go`: mounts (`/proc`, `/sys`, `/dev`, a tmpfs
      over writable paths, code disk read-only at `/work`, output disk at
      `/out`), `eth0` at 172.16.0.2 with no default route and IPv6 off, then
      runs `cc-claude` or `cc-selftest` as `agent` through `setpriv`, and writes
      `exit-code` to `/out`. Mounts and commands go through an interface, so
      tests use fakes.
  - _Done:_ the `Sys` interface (mount, files, device read, sync) plus
    `system.Runner`, which gained `Stdout`/`Stderr` writers, `Dir` and `Chroot`
    (`PrivRunner` refuses them on root commands). Drive order: `vda` root, `vdb`
    config, then for a review `vdc` code and `vdd` output, for a selftest `vdc`
    output. The code disk mounts `ro,nosuid,nodev,noexec,noload` (no journal
    replay); tmpfs covers `/tmp`, `/run`, `/var/tmp` and the agent's 0700 home.
    The agent runs as uid 10001 via
    `setpriv --clear-groups --no-new-privs --inh-caps=-all --bounding-set=-all`,
    with a clean environment holding only `PATH`, `HOME` and `CC_*` settings;
    the init (as root) opens `/out/stream.jsonl` and `/out/stderr.log` (0600),
    so the agent can't touch `/out` itself. **For M9:** attach drives in that
    order, and cap the copies off `/out`, since the guest caps nothing but the
    disk size.
- [x] `internal/guestinit/build.go`: as root, copy the base tree from the
      squashfs onto the target disk (keeping owners, modes, setuid and links),
      add the inputs disk's files (`claude`, the plugin, `cc-claude`,
      `cc-selftest`, `cc-guestinit` itself, the CA via
      `update-ca-certificates`), create the `agent` user, and set git's
      `safe.directory`. **Tests:** against a temp-dir tree with fakes for the
      privileged calls.
  - _Done:_ the squashfs lacks `cc-guestinit`, so **the build VM boots a
    one-file initramfs** holding it as `/init` (as in spike S1) and mounts its
    own disks: `vda` the squashfs (read-only), `vdb` config, `vdc` inputs
    (`ro,nosuid,nodev,noexec,noload`), `vdd` the blank target. `CopyTree` keeps
    owners, modes (chmod after chown, so setuid survives), symlinks, hard links,
    device nodes and times, never follows or overwrites anything in the target
    (`O_EXCL|O_NOFOLLOW`, proved by dropping the flags), and drops extended
    attributes, so file capabilities go. Inputs land root-owned with fixed
    modes: `claude`, `cc-claude` and `cc-selftest` in `/usr/local/bin`,
    `cc-guestinit` in `/usr/local/sbin` (review VMs boot it with `init=`), the
    CA in `/usr/local/share/ca-certificates/codecheck-sandbox.crt`, the plugin
    (plain files and folders only) in `/opt/codecheck/plugin`. The target's own
    `useradd` (uid 10001) and `update-ca-certificates` run chrooted with `/dev`
    and `/proc` bound in. A marker, `/etc/codecheck-sandbox/build-complete`,
    holds the run id. **Deviation:** `safe.directory` moves to `cc-claude`,
    since `GIT_CONFIG_NOSYSTEM=1` ignores `/etc/gitconfig`. **For M10:** pack
    the initramfs (a newc cpio of `/init`), lay out the inputs disk with those
    names, check the marker with `debugfs` and hash the disk on the host.
- [x] `cmd/cc-guestinit/main.go`: static build (`CGO_ENABLED=0`); runs as PID 1,
      picks the mode, and always ends with `reboot(LINUX_REBOOT_CMD_RESTART)`,
      even after a panic (spike S1: power-off hangs). **Test:** mode dispatch
      and the deferred reboot through an injectable syscall.
  - _Done:_ the logic lives in `guestinit.RunInit` (dispatch, then sync and
    reboot from a deferred function that also recovers a panic); `main` adds a
    guard that refuses any PID but 1, so running it on a host by mistake can't
    reboot anything. A test builds it with `CGO_ENABLED=0` and checks the ELF
    has no interpreter or shared libraries. The init doesn't reap orphaned
    grandchildren; a VM lives for one job, so zombies cost nothing before the
    reboot. **For M10:** build it the same way and append `panic=1` to the boot
    arguments, so a failed reboot still ends the VM.
- [x] `cc-claude`:
  - fixed flags (spec §6):
    `-p --restricted --strict-mcp-config --tools Read,Grep,Glob,Agent,Skill --allowedTools Read,Grep,Glob,Agent,Skill --plugin-dir … --permission-mode dontAsk --max-budget-usd … --no-session-persistence --output-format stream-json --verbose`;
    never `--bare`
  - the placeholder key, `NODE_EXTRA_CA_CERTS`, the telemetry variables, and
    `GIT_CONFIG_NOSYSTEM=1` and `GIT_CONFIG_GLOBAL=/dev/null`
  - ~~in diff mode, a git allowlist that excludes `--ext-diff`, `--output` and
    `-c`~~ (replaced by spec amendment 9)
  - _Done:_ `assets/guest/cc-claude`, a bash script embedded as `assets.Guest`.
    It checks `CC_MAX_BUDGET_USD`, `CC_SCOPE` and `CC_BASE` against the same
    patterns as the config parser, puts the prompt straight after `-p`
    (`--allowedTools` takes several values and would swallow a trailing prompt),
    runs in `/work/src`, and sets `safe.directory` through `GIT_CONFIG_COUNT`
    (the command scope, which git trusts). **Spec amendment 9** (the
    maintainer's choice, 2026-11-21): the diff prompt points Claude at
    `/work/change/diff.patch` and `/work/change/status.txt`, so the tool list
    never grows. Tests run the script against a stand-in `claude` and check the
    argument vector and environment, plus a text check for `--restricted`,
    `--strict-mcp-config`, the exact `--tools` list, and no `--bare` or `Bash`;
    disabling the budget check failed two of them.
- [ ] `cc-selftest`: the eight original probes, plus other host ports, IPv6
      link-local, a Docker-published port when present, DNS, and a spoofed
      source address using `ip addr add` (which the probe should not manage to
      do as `agent`).
- [ ] `assets/guest/vm.json.tmpl` for review and build VMs, with a golden test.
      The build VM has no `network-interfaces` entry; the boot arguments never
      repeat `pci=off` or `virtio_mmio` (spike S2).
- [ ] `assets_test.go`: when `shellcheck` exists, run it on every script;
      otherwise skip with a message. Also check that `cc-claude` carries
      `--restricted`, `--strict-mcp-config` and the exact `--tools` list, and
      never `--bare`.

## M9. Launch helper (`cmd/cc-launch`, `internal/launch`)

- [ ] `launch/runid.go`: run ids must match
      `^cc-[0-9]{8}-[0-9]{6}-[a-z0-9]{6}$`. The run folder sits at
      `/var/lib/codecheck-sandbox/runs/<id>`, and only the CLI writes there,
      through `sudo tee`. **Tests:** valid and invalid ids, and path traversal.
- [ ] `launch/verify.go`, in order:
  1. the lock
  2. the kernel and root disk SHA-256 against
     `/var/lib/codecheck-sandbox/images.sha256`
  3. the rule hash against `rules.sha256`
  4. the owner of the `172.16.0.1:3180` listener, from `/proc/net/tcp`, then the
     socket inode, the pid, `/proc/<pid>/cgroup` and the systemd unit
     `codecheck-proxy.service`
  5. the run disks: size caps and the ext4 magic number

  **Tests:** each failure path, using `MemFS` and `FakeRunner`.

- [ ] `launch/jail.go`: stage the chroot with read-only bind mounts (shared
      images) and copies (run disks), then build the
      `systemd-run --scope -p RuntimeMaxSec=… jailer …` argv from the spike S1
      flags. **Golden test** of the argv.
  - Keep `--new-pid-ns`; the jailer returns at once, so wait for the scope's
    `cgroup.events` to show `populated 0` before cleanup (spike S1, option b).
    **Test:** cleanup never starts while the fake cgroup still reports
    `populated 1`.
- [ ] `launch/extract.go`: for each file in a fixed list (`transcript.jsonl`
      capped at 200 MB, `stderr.log` at 5 MB, `exit-code` at 16 bytes),
      `debugfs -R "cat /<f>"`, run as `nobody` through `setpriv`. Then clean up
      the chroot and the run folder. **Tests:** caps and the file list.
- [ ] `cmd/cc-launch/main.go`: a single positional run id; it refuses anything
      else and exits with documented codes.
- [ ] `assets/host/sudoers`:
      `%<user> ALL=(root) NOPASSWD: /usr/local/lib/codecheck-sandbox/bin/cc-launch`.
      `LauncherStep` installs the binary root-owned with mode 755, and validates
      the sudoers file with `visudo -cf` before moving it into place.

## M10. Guest build (`internal/guest`)

- [ ] `guest/build.go` (no network at any point):
  1. Download and verify the base, kernel, Firecracker and Claude Code pins
     (M5).
  2. Create a blank `target.ext4` (3 GB) and a read-only inputs disk: the
     `claude` binary, the plugin from `git archive`, `cc-guestinit`,
     `cc-claude`, `cc-selftest` and the CA certificate. Config block mode
     `build`.
  3. Boot the builder through `cc-launch build <id>`. This separate subcommand
     uses the build VM template only, which has no network device; it takes the
     same lock as reviews. Its own argv validation and tests go into M9.
  4. Check the result, then install the kernel and root disk into
     `/var/lib/codecheck-sandbox/` and write `images.sha256`.

  Progress events come from the streamed serial log. **Tests:** sequence and
  failure handling with fakes; the real build runs in M15.

- [ ] `GuestBuildStep`, whose `Check` passes when the image hashes match and the
      pins have not changed since the last build.

## M11. Review pipeline (`internal/review`)

- [ ] `review/source.go`:
  - Clone a URL or local path into `~/codecheck-work/<name>-<ts>` (`--no-local`
    for paths).
  - `--pr N` fetches `pull/N/head`.
  - `--base REF`, or the merge base with the default branch, sets diff mode.
  - In diff mode, after `harden.go`, write `git diff <base>` and
    `git status --porcelain` (run through `system.Runner` with a clean
    environment) to `change/diff.patch` and `change/status.txt` beside the clone
    (spec amendment 9).

  **Tests** use local git repos in `t.TempDir()`.

- [ ] `review/harden.go`: rewrite `.git/config` to a clean minimum (core
      settings and remote only), and remove `.git/hooks`. **Test:** a planted
      `diff.external` and a hook both disappear.
- [ ] `review/disks.go`: the code disk via `mkfs.ext4 -d` (unprivileged), sized
      from `du` plus 30%, with the clone at `src/` and any diff files at
      `change/`; the config block from `guestinit.Encode` (fixed 4 KB, no
      secrets: mode, budget, scope, baseline; the prompt lives in `cc-claude`);
      and a blank output disk. **Tests** run against small fixtures when
      `mkfs.ext4` exists.
- [ ] `review/postrun.go`, from the extracted files:
  - the result event (`subtype`, `is_error`, `total_cost_usd`)
  - refusal patterns
  - new proxy denials since the run started
  - the exit code
  - `report.md` and `summary.json`

  **Tests** use fixture transcripts: success, budget stop, refusal and empty.

- [ ] `review/run.go`: write the run folder through `sudo tee`, call
      `sudo -n cc-launch <id>`, and stream status events from the VM's serial
      log.
  - Live status shows elapsed time and VM state (spec §2, as amended); cost
    appears in the final summary from `postrun.go`.

## M12. Interface (`internal/theme`, `internal/tui`)

- [ ] `theme/theme.go`: Lip Gloss adaptive palette, plus the styles for sidebar,
      panels, ✓ ✗ ⣾ · and keys. Test: renders at the ASCII profile.
- [ ] `tui/app.go`: the root model that routes engine events to screens. Quit
      through `q` or Ctrl-C asks for confirmation during root steps.
- [ ] `tui/sidebar.go`, `tui/steplist.go`, `tui/confirm.go` (commands, the `d`
      key opens a viewport of files, Enter, `s`, `q`), `tui/progress.go`,
      `tui/failure.go` (cause, fix, `r` retry). Each gets a `teatest` golden
      test at 100×30.
- [ ] `tui/sudo.go`: on a `NeedsSudo` event, hand over through
      `tea.ExecProcess(sudo -v)`, then start the keepalive. A unit test checks
      the message flow. The manual checklist goes in `docs/manual-tests.md`.
- [ ] `tui/forms.go`: `huh` forms for budget, reports folder, VM size and API
      key. The API key field uses `EchoModePassword`, and its validator calls
      `/v1/models` through an injectable client. Tests cover the validators.
  - A telemetry consent step: default "No", with a viewport showing a sample
    payload rendered from real event structs. **Test:** the default stays off,
    and the sample holds no field outside the event types.
- [ ] `tui/doctor.go` (a results table), `tui/review.go` (status, then the
      Glamour report), and `tui/uninstall.go` (a confirm list). Golden tests.
- [ ] `tui/plain.go`: a line printer for `--plain`, `--yes` or when no TTY
      exists. Golden test of a full fake run.

## M13. Commands (`internal/cli`)

- [ ] `setup`: builds the step list (phases from spec §7) and runs it through
      the engine, with the TUI or plain mode. `--yes` takes its answers from
      flags, `config.toml` and `CODECHECK_API_KEY`, and fails fast when anything
      goes missing. **Test:** a full dry run on a fake host prints every planned
      command and changes nothing (`FakeRunner` sees no `Root` writes).
- [ ] `doctor`: runs every `Check`, the image and rule hashes, and template
      drift (re-render, then compare with installed). It exits 1 on any failure.
      **Tests:** healthy and drifted fake hosts.
- [ ] `doctor --bundle [--out FILE]`: list the files first, then write a
      redacted `.tar.gz` of recent logs, `doctor` output, versions and the
      environment profile. **Test:** a planted key in a log never reaches the
      archive.
- [ ] `telemetry on|off|status`: change and show consent; `off` deletes the
      install id and the queue. **Tests:** each subcommand against a temp config
      folder.
- [ ] `selftest`, `canary`: a run in mode `selftest`, or the canary repo (from
      `assets/canary/`) as a review with budget 5; then their pass rules (spec
      §7). **Tests** use fixture outputs.
- [ ] `review`: flags from spec §8. **Test:** argument validation, and the
      run-folder contents.
- [ ] `rebuild [--commit SHA]`: runs `GuestBuildStep`, forced.
- [ ] `uninstall [--purge]`: reverses the manifest, falls back to a footprint
      scan, and keeps reports unless `--purge`. **Test:** a fake host after
      setup ends clean, and adopted items stay.
- [ ] `config.go`: `~/.config/codecheck-sandbox/config.toml` (budget, reports
      folder, VM size, CodeCheck commit, telemetry). Hand-rolled TOML subset or
      `BurntSushi/toml`. **Test:** round trip.

## M14. Docs

- [ ] Complete every `docs/usage/` page against the built tool: requirements,
      install (`go install ./cmd/...`, then `codecheck-sandbox setup`), the
      setup walkthrough with screenshots or captured screens, each command and
      flag, unattended mode, troubleshooting by failing step, privacy and
      telemetry (the exact event list), and uninstall. Remove every "planned"
      marker or keep it only for deferred items.
- [ ] Complete `docs/architecture.md`: diagrams of the host footprint, the boot
      and review flow, and trust boundaries; check each claim against the code.
- [ ] `README.md`: final quick start and limits (Linux with KVM, x86_64, one VM
      at a time). Fill in `CHANGELOG.md` `[Unreleased]`.
- [ ] Update the maintainer's private deployment plan document: a note at the
      top that the CLI now automates it, plus the proxy and key-injection
      change. (The maintainer has the link.)

## M15. Integration and finish

- [ ] `integration/` (`//go:build integration`). On this laptop, with sudo:
  1. `setup --yes`
  2. `doctor`
  3. `selftest`, where every escape probe passes
  4. `canary`
  5. `uninstall`
  6. `doctor` again, reporting nothing installed

  Run:
  `CODECHECK_API_KEY=… go test -tags integration ./integration/ -timeout 90m`.

- [ ] Manual sudo-handover checklist (`docs/manual-tests.md`), worked through
      and ticked.
- [ ] The pilot:
      `codecheck-sandbox review https://github.com/juice-shop/juice-shop --prompt "Run a security review of ./routes" --budget 25`.
      Record cost, time and findings in the spikes file.
- [ ] Final checks:
  - `go test ./... && go vet ./...`
  - `gofmt -l .` prints nothing
  - `npx --yes prettier@3.9.10 --check .`
- [ ] Run `/code-review medium` and fix what it finds.
- [ ] Ask the maintainer before pushing and opening the PR.
- [ ] The PR to `main` of ModrichLabs/codecheck-sandbox, with a summary, test
      evidence and the 🤖 line. The maintainer reviews and merges, then decides
      whether to tag `v0.1.0`.

## Deferred (not in this plan)

- aarch64.
- A nested-KVM QEMU test matrix for Ubuntu and Fedora (spec §9, "later").
- Parallel reviews.
- Release binaries.

## Verification

Done means:

- `go test ./...` and the integration suite pass on this laptop.
- `codecheck-sandbox setup --dry-run` changes nothing.
- A real `setup` passes `doctor` and every `selftest` probe.
- The canary passes.
- The Juice Shop pilot produces a report.
- `uninstall` returns `doctor` to "nothing installed".
