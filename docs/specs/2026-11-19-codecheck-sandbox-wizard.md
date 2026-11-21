# Spec: codecheck-sandbox setup wizard and lifecycle CLI

Status: approved 2026-11-19; amended the same day after the spikes (see
"Amendments after the spikes").

## Context

[CodeCheck](https://github.com/Commonwealth-Bank-of-Australia/CodeCheck), an
MIT-licensed Claude Code plugin from the Commonwealth Bank of Australia, runs
LLM-driven security reviews of source code. Its read-only guardrails exist only
as prompts, so it needs outside containment. The maintainer's safe deployment
plan (a private document) runs every CodeCheck review inside a jailed
Firecracker microVM whose only network path leads to a proxy that allows
Anthropic's API alone. By hand, that takes about 40 steps across five phases and
fits only one Arch laptop. This design turns it into one Go binary with a modern
terminal interface. It guides a person through setup, checks and installs what
has gone missing, collects what it needs, and runs reviews afterwards, on any
Linux machine with KVM.

**Decisions made with the maintainer**

- Linux with KVM only, Firecracker only, with distro-aware guidance.
- Go, as a single binary, with a full-screen Charmbracelet Bubble Tea interface.
- A standalone repository, `ModrichLabs/codecheck-sandbox`, owned by Modrich
  Labs under Apache-2.0. CodeCheck stays a pinned, third-party dependency (moved
  out of a CodeCheck fork on 2026-11-20).
- Root steps default to "show, confirm, run". `--yes` runs them unattended;
  `--dry-run` only prints.
- Full lifecycle commands: `setup`, `doctor`, `selftest`, `canary`, `review`,
  `rebuild` and `uninstall`.
- The guest root disk gets built inside a Firecracker builder VM, with no Docker
  or Podman.

**Changes from the deployment plan, following the independent review** (summary
at the end):

- A small proxy built into the binary replaces squid. It also **injects the API
  key**, so the key never enters the VM.
- The guest build runs with no network at all.
- Every launch goes through a root-owned helper that re-checks the firewall
  rules and image hashes before it boots anything.

The task plan lives in `docs/plans/2026-11-19-codecheck-sandbox.md`, and the
spike findings in `docs/plans/2026-11-19-codecheck-sandbox-spikes.md`.

## Approach

A **step engine**. Each piece of setup becomes a `Step` with four methods:

- `Check`: does this already hold?
- `Plan`: the exact commands and file contents it would apply.
- `Apply`: make the change.
- `Undo`: reverse it.

The wizard walks the steps in order; `doctor` runs only `Check`; `--dry-run`
prints `Plan`; `uninstall` runs `Undo` in reverse. Every command goes through
one `Runner` interface, so tests can fake it. The terminal interface only shows
state: the engine sends progress messages to it.

I considered and rejected two other approaches. An imperative wizard (one
function per phase) would need separate code for dry-run, doctor and uninstall.
A shell-template generator would lose typed checks and testability.

## Design

### 1. Repository layout and the CodeCheck dependency

- Go module `github.com/ModrichLabs/codecheck-sandbox` at the repository root.
- Conventional Commits, Keep a Changelog, and Semantic Versioning. Every release
  stays pre-1.0 (`0.y.z`) until the maintainer declares 1.0.0; `-dev` marks
  unreleased builds.
- Licence Apache-2.0, copyright Modrich Labs. `NOTICE` credits CodeCheck (MIT,
  Commonwealth Bank of Australia); the guest image carries CodeCheck's own
  `LICENSE.txt` beside the plugin.
- **The CodeCheck dependency:** `internal/pins` holds a git URL and a full
  commit id, by default
  `https://github.com/Commonwealth-Bank-of-Australia/CodeCheck` at
  `3231a0def630ff7f1529de47af2ea763d23b8217` (tag `v0.0.1`). The host fetches
  that commit into a cache with plain git (no checkout hooks), confirms the
  commit id, then runs `git archive <commit> .claude-plugin skills LICENSE.txt`
  for the guest build. `rebuild --commit` and a `codecheck_repo` setting let
  users point at a mirror, such as the `ModrichLabs/CodeCheck` fork. No code
  from this repository enters a VM except the guest binaries and scripts.

```
./
  cmd/codecheck-sandbox/   the CLI
  cmd/cc-launch/           root helper: verifies, then boots one VM under the jailer
  cmd/cc-guestinit/        static PID 1 for the builder and review VMs
  internal/
    cli/  tui/  theme/     subcommands; Bubble Tea screens; Lip Gloss styles
    step/                  Step interface, engine, manifest
    system/                Runner (exec, privilege), filesystem, test fakes
    platform/  distro/     KVM, CPU, RAM, disk, systemd, firewall managers, SELinux/AppArmor; package mapping
    pins/                  versions, URLs, checksums, GPG fingerprints
    hostnet/               taps, nftables rules and their integrity hash
    proxy/                 the built-in CONNECT proxy with API-key injection
    guest/  vm/            builder VM and root disk; per-run disks, result extraction
    review/                fresh clone, PR checkout, git hardening, post-run checks
  assets/                  go:embed: cc-claude, cc-selftest, units, rule templates
  docs/
    architecture.md        design and architecture for readers and contributors
    usage/                 requirements, install, setup, commands, troubleshooting, privacy
    specs/  plans/         this spec, the task plan and the spike findings
  README.md  CHANGELOG.md  CONTRIBUTING.md  SECURITY.md  CODE_OF_CONDUCT.md
  LICENSE  NOTICE  .github/ (issue and PR templates)
```

### 2. Terminal interface (Charmbracelet)

All Charm packages come from their v2 lines (`charm.land/bubbletea/v2`,
`charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `charm.land/huh/v2`,
`charm.land/glamour/v2`, and `teatest/v2`).

| Package         | Used for                                                                                                   |
| --------------- | ---------------------------------------------------------------------------------------------------------- |
| `bubbletea`     | The app loop: one root model that switches between screens                                                 |
| `bubbles`       | Spinner, progress bar (downloads, build), viewport (command previews, logs), table (`doctor`), help footer |
| `huh`           | Forms: budget, reports folder, VM size, API key (hidden); accessible mode for screen readers               |
| `lipgloss`      | Layout, sidebar, panels, status colours that adapt to light and dark terminals                             |
| `glamour`       | Renders `report.md` at the end of `review`                                                                 |
| `log`           | Line output in plain mode and the debug log                                                                |
| `x/exp/teatest` | Golden-output tests of each screen                                                                         |

```
╭─ CodeCheck Sandbox setup ──────────────────────────────────────────╮
│ ✓ Welcome          │  Host network                                 │
│ ✓ Platform         │  ✓ fcjail user                                │
│ ✓ Components       │  ⣾ Review tap cc-tap0                         │
│ ✓ Your details     │  · Firewall rules (nftables)                  │
│ ▸ Host network     │  · Egress proxy service                       │
│ · Guest build      │ ╭ Needs root: review before running ────────╮ │
│ · Verify           │ │ ip tuntap add dev cc-tap0 mode tap user … │ │
│ · Summary          │ │ write /etc/codecheck-sandbox/rules.nft    │ │
│                    │ ╰───────────────────────────────────────────╯ │
│                    │  [Enter] run   [d] show files   [s] skip   [q] │
╰────────────────────────────────────────────────────────────────────╯
```

- **Privilege and the terminal.** Before the first root step, the app hands the
  terminal to `sudo -v` through `tea.ExecProcess`, so the password prompt works
  normally. A goroutine then refreshes the login every 60 seconds with
  `sudo -n -v`. Every root command runs as `sudo -n` with stdin from
  `/dev/null`, so it can never prompt while the interface owns the screen. If
  the login expires anyway, the engine pauses and repeats the `sudo -v`
  handover. It also detects sudo-rs, `doas` and `run0`, and never pipes a
  password.
- **Files go in as previewed.** Root files get created with their final mode
  (`install -m`), then filled by `dd status=none` through the privilege tool
  from the same in-memory bytes the confirm panel showed (amendment 8).
- **Plain mode.** Without a TTY, or with `--plain` or `--yes`, the same engine
  prints `charmbracelet/log` lines.
- **Live status.** `review` shows elapsed time and the VM state from the serial
  log. The stream-json transcript only leaves the VM after the run, so cost and
  sub-agent activity appear in the final summary, followed by the rendered
  report.

### 3. Host footprint (root-owned unless noted)

| Item                                                                                                                                                                                        | Where                                                                         |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Firecracker and jailer from pinned GitHub release binaries, with SHA-256 checks                                                                                                             | `/usr/local/lib/codecheck-sandbox/bin/`                                       |
| `cc-launch` helper, plus a `NOPASSWD` sudoers drop-in for it alone                                                                                                                          | `…/bin/cc-launch`, `/etc/sudoers.d/codecheck-sandbox`                         |
| `cc-launch`'s lock, recreated at every boot by a `tmpfiles.d` entry                                                                                                                         | `/etc/tmpfiles.d/codecheck-sandbox.conf`, `/run/codecheck-sandbox/lock`       |
| Kernel, root disk, expected hashes, root-owned manifest                                                                                                                                     | `/var/lib/codecheck-sandbox/`                                                 |
| `fcjail` system user; jailer chroot base                                                                                                                                                    | `/srv/jailer`                                                                 |
| Review tap `cc-tap0` (172.16.0.1 ↔ .2), IPv6 off, unmanaged by NetworkManager                                                                                                               | `codecheck-net.service`                                                       |
| nftables table `inet codecheck`: `cc-tap0` may reach only `172.16.0.1` on proxy port 3180; a final drop for every other packet from `cc-tap*`; all forwarding to and from `cc-tap*` dropped | `/etc/codecheck-sandbox/rules.nft`                                            |
| Egress proxy: `codecheck-sandbox proxy` as a `DynamicUser=` systemd service, listening on `172.16.0.1:3180` only                                                                            | `codecheck-proxy.service`                                                     |
| Distro packages: nftables, e2fsprogs, squashfs-tools, git, iproute2, gnupg (no squid)                                                                                                       | Through the distro's package manager                                          |
| Settings, and the debug log with the key redacted (user-owned)                                                                                                                              | `~/.config/codecheck-sandbox/`, `~/.local/state/codecheck-sandbox/log/`       |
| The API key, readable only by the proxy service                                                                                                                                             | `/etc/codecheck-sandbox/api-key` (mode 600, loaded through `LoadCredential=`) |

### 4. The proxy and the key (the main security upgrade)

The proxy runs as a separate service and needs no extra packages:

1. It accepts `CONNECT api.anthropic.com:443` only, and only from `cc-tap0`'s
   guest address; it refuses everything else and logs it.
2. It terminates TLS using a sandbox CA, whose certificate gets baked into the
   guest image (`NODE_EXTRA_CA_CERTS`).
3. It replaces the guest's placeholder key with the real key, then forwards over
   real TLS to Anthropic.
4. It rejects any request that carries a different key.

Claude Code sends the key only in `x-api-key` (spike S4). Besides
`POST /v1/messages`, it calls `GET /api/claude_code/settings` and
`GET /api/claude_code/policy_limits` at start-up; the proxy lets both through.

This gives three results:

- The VM never holds a usable key.
- An attacker who gains code execution cannot exfiltrate using their own
  Anthropic key.
- Spend stays bounded per run by `--max-budget-usd` and a proxy-side
  byte/request cap.

Telemetry stays off in the guest (`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`,
`DISABLE_AUTOUPDATER=1`), so denied requests mean something.

### 5. The launch helper (`cc-launch`)

The CLI calls `sudo -n cc-launch <run-id>` for every review, selftest and
canary. The helper accepts no paths and no flags that change the sandbox. Before
booting, it:

1. Takes a lock on `/run/codecheck-sandbox/lock`, so only one VM runs at a time
   and none starts during `uninstall`.
2. Checks the SHA-256 of the root-owned kernel and root disk.
3. Hashes `nft -j list table inet codecheck` and compares it with the expected
   rules; it refuses on any difference, such as after `nftables.service` flushed
   them.
4. Confirms that the proxy service, not some other process, owns
   `172.16.0.1:3180`.
5. Stages the jail with read-only bind mounts (no 1 GB copy) and launches
   `jailer --cgroup-version 2` through `systemd-run --scope -p RuntimeMaxSec=…`,
   so a timeout kills the whole VM cleanly.
6. Afterwards, copies a fixed list of files off the output disk with size caps,
   using `debugfs -R "cat …"` in a throwaway unprivileged process.

The code and config disks come from the CLI in a run folder that `cc-launch`
copies and validates (size limits, ext4 superblock check).

### 6. Building the guest (`setup` and `rebuild`)

- **Base:** the Ubuntu 24.04 cloud-image squashfs, checked against Ubuntu's
  GPG-signed `SHA256SUMS`.
- **Kernel:** Firecracker's guest kernel, mirrored into a release of this
  repository with its origin recorded. That gives a stable URL; the trust stays
  an accepted, documented risk.
- **Claude Code:** a single native binary (spike S3). The **host** downloads the
  pinned `@anthropic-ai/claude-code-linux-x64` npm tarball over HTTPS, checks
  its `sha512` integrity, extracts `package/claude` and checks the binary's
  SHA-256 pin. The host never runs npm, Node or package scripts. The guest needs
  no Node and no `rg`; Claude Code's bundled search serves Grep.
- **No builder network.** The builder VM boots from the squashfs with **no
  network device at all**. Its inputs arrive on a read-only disk: the binary,
  the plugin from `git archive`, `cc-guestinit`, `cc-claude`, `cc-selftest` and
  the sandbox CA certificate.
- **Inside the builder,** `cc-guestinit` in build mode, as root:
  1. Copies the base tree onto a blank ext4 disk.
  2. Adds the inputs, the CA certificate (`update-ca-certificates`), the `agent`
     user and git's `safe.directory`.
  3. Writes the result's hashes and reboots.
- **Result:** the disk lands in `/var/lib/codecheck-sandbox/` with its hashes.
- **Guest init:** `cc-guestinit`, a static Go binary, runs as PID 1 in both the
  builder and review VMs and picks its mode from the config device. It mounts
  what it needs, reads run settings from the raw config device as root and hands
  them on through memory (so permissions never depend on uids in an image built
  without root), runs the job, then calls the reboot syscall: Firecracker exits
  on a guest reboot, while a power-off hangs (spike S1).
- **How Claude runs:** `cc-claude` starts
  `claude -p --restricted --strict-mcp-config --tools Read,Grep,Glob,Agent,Skill --allowedTools Read,Grep,Glob,Agent,Skill --plugin-dir … --permission-mode dontAsk --max-budget-usd … --no-session-persistence --output-format stream-json --verbose`.
  It never passes `--bare`, which removes Grep, Glob, Agent and Skill whatever
  `--tools` says (spike S4). `--restricted` ignores user, project and local
  settings and removes the command-running tools and WebFetch; `--tools` fixes
  the exact tool set.
- **Diff mode** (amendment 9): Claude runs no git in the guest, since it has no
  shell. On the host, `review` runs `git diff <baseline>` and
  `git status --porcelain` in the hardened clone and writes their output to
  `/work/change/diff.patch` and `/work/change/status.txt` on the code disk, with
  the clone itself at `/work/src`. `cc-claude`'s prompt names the baseline and
  both files. `review` still writes a clean `.git/config`, and `cc-claude` sets
  `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null` and `safe.directory`
  through `GIT_CONFIG_COUNT`, for any git Claude Code runs on its own.

### 7. The setup wizard flow

1. **Welcome.** What setup will do. A reminder that reviews send code to
   Anthropic, that the code needs authorisation, and to use a dedicated,
   spend-limited workspace key.
2. **Platform checks.**
   - Linux x86_64 (aarch64 deferred).
   - KVM usable by `fcjail`.
   - systemd and cgroups v2.
   - A privilege tool (sudo, sudo-rs, doas or run0).
   - At least 8 GB RAM and 10 GB free disk.
   - Firewall managers detected (firewalld, ufw, Docker), plus SELinux or
     AppArmor mode.
3. **Components.** Detect the distro family (Arch, Debian/Ubuntu, Fedora/RHEL,
   openSUSE), then show and apply the install command. Unknown distros get the
   package list and a pause.
4. **Your details.**
   - The API key: hidden input, tested against `/v1/models` through the host.
   - The default budget (US$25).
   - The reports folder.
   - VM size (scaled to the host).
   - The CodeCheck commit, defaulting to `3231a0d`; changes come later through
     `rebuild --commit`.
   - Anonymous usage and error reporting (§11): off unless chosen, with a sample
     payload shown.
5. **Host setup.** Root steps, each previewed and confirmed:
   - the jail user, tap, rules and proxy
   - `cc-launch` and its sudoers rule
   - firewalld or ufw allowances, NetworkManager unmanaged-device config,
     SELinux labels where needed
6. **Guest build,** with a progress bar and a streamed log.
7. **Verify.**
   - `selftest`, with the probes extended to try other host ports, IPv6
     link-local, a Docker-published port, DNS and a spoofed source address.
   - The optional `canary` (about US$1–5).
8. **Summary,** plus a warning about `docker` group membership.

Re-running `setup` skips each step whose `Check` passes. Each `Apply` checks
before it acts, and the nft rules load as one `destroy table` and add
transaction.

### 8. Commands, unattended mode, uninstall

- **`doctor`:** every `Check`, image hashes, rule hash and template drift, shown
  as a table. It exits non-zero on any failure.
- **`review <path|url> [--pr N | --base REF] [--prompt] [--cwe] [--budget]`:**
  fresh clone and PR checkout on the host, git hardening, then `cc-launch`.
  Post-run checks cover the exit code, refusals and the proxy's denial log; then
  it writes `report.md` (folder mode 700).
- **`selftest`, `canary`, `rebuild [--commit]`:** as described above.
- **`doctor --bundle`** and **`telemetry on|off|status`:** see §11.
- **`--yes`:** reads the key from `CODECHECK_API_KEY` (never a flag) and the
  other answers from flags or `config.toml`; it refuses to run if an answer goes
  missing. `--dry-run` changes nothing.
- **`uninstall`:**
  - Reverses the code-defined footprint, using the root-owned manifest as hints
    checked against a path allowlist.
  - Removes only items the manifest marks "created", never "adopted" ones such
    as packages or users that existed before.
  - Falls back to a footprint scan when the manifest has gone missing.
  - Keeps reports unless `--purge` gets passed.
- **Manifest schema** carries a version and an "installed by" record, so later
  releases can migrate.

### 9. Testing

- **Unit (`go test ./...`):**
  - Steps against a fake Runner and a temp-dir filesystem.
  - Distro detection from sample `os-release` files.
  - Golden templates for rules, units, the sudoers line and `vm.json`.
  - The proxy, against a fake upstream: allow, deny, key swap, wrong-key
    rejection.
  - A redaction test that greps every log for the key.
- **TUI (`teatest`):** each screen from scripted keys and fake engine messages,
  rendered as golden output with the colour profile pinned to ASCII. The
  `ExecProcess` sudo handover gets checked by hand from a checklist.
- **Integration (`-tags integration`, needs KVM):**
  - On this laptop: `setup --yes`, the escape-probe selftest, a canary review
    and `uninstall`, finishing with `doctor` clean.
  - Later: the same suite in disposable nested-KVM QEMU guests for Ubuntu and
    Fedora.

### 10. Spikes at the start of the implementation plan

Done; findings in `docs/plans/2026-11-19-codecheck-sandbox-spikes.md`. They
covered the Firecracker v1.18.0 jailer under `systemd-run`, the Ubuntu squashfs
as a root disk, the Claude Code install, TLS interception with key injection,
and the sudo handover.

### 11. Logging, diagnostics and telemetry (approved 2026-11-19)

Three layers. The first two always run and never leave the machine; the third
needs the user's consent.

1. **Local structured logs.** `log/slog` JSON lines, through the `Redactor`, in
   `~/.local/state/codecheck-sandbox/log/`, rotated at 10 MB with five files
   kept. `--debug` raises the level and also prints them through
   `charmbracelet/log`. Every command, step outcome, root command (argv, exit
   code, duration) and `cc-launch` exit code gets a line.
2. **Run history and diagnostics.** Each review writes `summary.json` beside
   `report.md`. `doctor --bundle` writes a redacted `.tar.gz` of recent logs,
   `doctor` output, versions and the environment profile, after showing the file
   list; the user shares it by hand.
3. **Remote telemetry: opt-in PostHog Cloud US.**
   - **Consent:** the wizard's "Your details" phase asks once and shows a sample
     of the exact payload; off unless the user says yes. Stored as `telemetry`
     in `config.toml`; `codecheck-sandbox telemetry on|off|status` changes it.
     `DO_NOT_TRACK=1`, `--no-telemetry`, `--yes` without an explicit
     `telemetry = true`, and a missing TTY with no config all mean off. `off`
     deletes the install id and any queued events.
   - **Identity:** a random install id (UUIDv4) only. Events carry
     `$process_person_profile: false` and `$geoip_disable: true`.
   - **Events** (typed Go structs; every field comes from an enum, a band or a
     version string, never free text):
     - _usage:_ `command_run`, `setup_phase_reached`, `step_finished` (step id,
       outcome, duration band), `review_finished` (mode full, diff or PR;
       outcome; duration and cost bands; sub-agent count band), CLI and Claude
       Code versions.
     - _errors:_ PostHog `$exception` events built by our code: error kind (a
       sentinel or type name, never the message), failing step id, and a stack
       of the module's own function names and file paths relative to the module
       root.
     - _environment profile_ (once per `setup` and `doctor`): distro family and
       version id, CPU arch, kernel major.minor, RAM and disk bands, firewall
       manager, security module and mode, privilege tool, Firecracker version.
     - _security signals_ (counts and booleans): proxy denials per run, selftest
       probes failed by id, `cc-launch` refusals by reason (image hash, rule
       hash, listener owner, lock, run disks).
   - **Never sent:** code, file contents, paths outside the module, repo names
     or URLs, PR numbers, prompts, findings, CWE ids, error messages, the API
     key, hostnames, usernames, IP addresses, exact costs or durations.
   - **Sending:** only from the host CLI. The VM has no route to PostHog, and
     `cc-launch` makes no network calls. Events queue in
     `~/.local/state/codecheck-sandbox/telemetry/queue.jsonl` (capped at 1 MB)
     and go in one batch at the end of a command through `posthog-go`, with a
     3-second timeout; failures drop silently into the local log. Telemetry
     never delays or fails a command.
   - **Library:** `github.com/posthog/posthog-go`, wrapped by
     `internal/telemetry` behind an interface with a no-op implementation. The
     project key (`phc_…`, write-only) and host (`https://us.i.posthog.com`)
     live in `internal/pins`. We do not use its slog integration or its debug
     images (they read `/proc/self/maps`, which holds full paths), and we turn
     feature-flag polling off.

## Amendments after the spikes (approved 2026-11-19)

1. **Go guest init** (S2): `cmd/cc-guestinit` replaces the `cc-init` shell
   script.
2. **Offline guest build** (S3): the host fetches and verifies the Claude Code
   binary; the builder VM has no network. This drops `cc-tap1`, the build proxy
   mode, apt, `snapshot.ubuntu.com`, npm, the lockfile and Node. The review
   outcome items below that mention them no longer apply.
3. **Live status** (S4): elapsed time and VM state during a run; cost in the
   final summary.
4. **No `--bare`** (S4): `--restricted` plus an exact `--tools` list.
5. **Charm v2** for every interface package.
6. **Logging, diagnostics and opt-in telemetry** (the maintainer's request):
   §11.
7. **Standalone repository** (2026-11-20): the project left the CodeCheck fork
   for `ModrichLabs/codecheck-sandbox` under Apache-2.0; CodeCheck becomes a
   pinned git dependency (§1). The project documentation set in §1's layout
   joins the plan's M1 and M14.
8. **Root file writes** (M2 review, approved 2026-11-20): `install -m <mode>`
   then `dd status=none` replace `sudo -n tee` then `chmod`, so a secret never
   sits under umask permissions or echoes back as command output. Root commands
   run as `<tool> -n -- <cmd>`, or
   `run0 --no-ask-password --setenv=LC_ALL=C -- <cmd>`.
9. **Host-computed diffs** (M8, approved 2026-11-21): CodeCheck's diff-aware
   review has sub-agents run `git diff` and `git status`, but `--restricted`
   with the fixed `--tools` list leaves Claude no shell. The host now writes
   both outputs to the code disk, and `cc-claude`'s prompt points Claude at
   them; Claude never gets Bash, and the planned git allowlist goes.

## Review outcome (independent Plan-agent review)

- **Accepted, all six must-fix items:**
  1. the build network now exists only during builds, with interface, address
     and port rules
  2. no distro squid at all
  3. the sudo handover through `ExecProcess`, plus the `cc-launch` helper
  4. the fail-closed rule-hash check before each boot
  5. root-built disks and settings read from the raw device
  6. root-owned images and manifest
- **Accepted from "should fix":** GPG-verified Ubuntu base;
  `npm ci --ignore-scripts`; apt through `snapshot.ubuntu.com`; capped
  `debugfs cat`; `systemd-run` lifecycle; bind mounts; locking; idempotence and
  created/adopted tracking; firewall, NetworkManager and SELinux steps;
  `LC_ALL=C` with JSON output (`ip -j`, `nft -j`); log redaction and schema
  version; git hardening; nested-KVM integration tests.
- **Accepted from "consider":** the built-in proxy, which I extended with key
  injection; deferring aarch64; dropping the commit diff summary; `sudo tee`
  from previewed bytes; checking KVM access for `fcjail`; the dedicated-key
  advice.
- **Declined:** the desktop keyring store. The key now lives only with the proxy
  service, which makes a keyring redundant.

## Verification (end to end, on this laptop)

1. `go test ./...` passes.
2. `setup --dry-run` prints the plan and changes nothing.
3. `setup` completes.
4. `doctor` shows all green, and `selftest` passes every escape probe.
5. `canary` passes.
6. `review` of Juice Shop `./routes` with `--budget 25` produces a report.
7. `uninstall`, then `doctor` reports nothing installed.
8. The full suite and `/code-review medium` run before the PR.
