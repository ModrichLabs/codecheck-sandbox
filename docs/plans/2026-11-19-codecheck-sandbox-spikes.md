# Spike findings: codecheck-sandbox

Results of the M0 spikes in
[the implementation plan](2026-11-19-codecheck-sandbox.md). Each entry records
what ran, what happened, and what changes for the spec or plan.

## S1: Firecracker basics (2026-11-19)

**Setup.**

- Firecracker v1.18.0 x86_64 release tarball, checksum verified
  (`1ac8eec8…6623cd`).
- CI guest kernel `vmlinux-6.1.155`, SHA-256
  `e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2`.
- A static Go `/init` packed as an initramfs.
- The jailer variants ran through `~/spike/fc/jailer-spike.sh`, as root, in a
  terminal.

**Findings**

1. **Latest release: v1.18.0**, not v1.17 (Arch packages 1.17). Pin release
   binaries (`firecracker-v1.18.0-x86_64`, `jailer-v1.18.0-x86_64`) and verify
   them against the release's `SHA256SUMS`. Distro packages lag and vary.
2. **CI artefacts stop at `v1.15`.** The S3 bucket has no
   `firecracker-ci/v1.16`–`v1.18` folders, and the v1.18 getting-started guide
   still picks the newest folder, `v1.15`. This confirms the review's warning:
   mirror the kernel into a release of the fork and pin it by SHA-256 (spec §6
   unchanged).
3. **Unprivileged boot works.** A normal user with access to `/dev/kvm` boots
   the kernel and initramfs in under a second with `--no-api --config-file`.
   Firecracker appends `pci=off` itself.
4. **The guest must reboot, not power off.** `reboot(RESTART)` with `reboot=k`
   makes Firecracker exit with code 0 at about 0.8 seconds. `reboot(POWER_OFF)`
   halts the guest and leaves Firecracker running until a timeout kills it.
   `cc-init` must end with a reboot (`reboot -f`, or the syscall).
5. **Jailer flags confirmed for v1.18:** `--id`, `--exec-file`, `--uid`,
   `--gid`, `--chroot-base-dir`, `--cgroup-version 2`, `--parent-cgroup`,
   `--cgroup <file>=<value>`, `--new-pid-ns`, `--netns`,
   `--resource-limit fsize=…|no-file=…`, `--daemonize`.
6. **Read-only bind mounts into the chroot work.** Kernel and initramfs
   bind-mounted with `remount,bind,ro` booted fine, and even root could not open
   them for writing. A per-run copy of the root disk can go.
7. **`--new-pid-ns` makes the jailer return at once.** The jailer forks
   Firecracker into a new PID namespace and its own process exits, so
   `systemd-run --scope` and the caller see exit 0 within a second while the VM
   keeps running. The spike's cleanup raced the running VMs (both still finished
   cleanly; no leftover processes, mounts or folders).
   - **Plan change for M9 (`launch/jail.go`):** pick one of these:
     - (a) Drop `--new-pid-ns`, so the jailer execs Firecracker in place and
       `systemd-run --scope` blocks until the VM exits. That costs only a PID
       namespace, since Firecracker's seccomp filters and the chroot stay.
     - (b) Keep it, and have `cc-launch` wait for the scope's cgroup to empty
       (`cgroup.events` `populated 0`) before cleanup, with `RuntimeMaxSec`
       still killing the scope at the timeout.

     Recommendation: (b). It keeps the isolation and makes waiting explicit.
     Cleanup must never start while the cgroup still holds a process.
8. **`Delegate=yes` made no difference** without `--cgroup` limits. Still
   untested: `--cgroup memory.max=…` under a scope, which likely needs
   `Delegate=yes` and `--parent-cgroup` pointing inside the scope. Test this in
   M9 with the integration suite.
9. **Running root steps:** sudo cannot prompt from Claude Code's `!` prefix (no
   TTY). Root spike scripts run in a separate terminal, which matches the
   design's choice of `tea.ExecProcess` for the password.

## S2: Ubuntu base (2026-11-19)

**Setup.**

- Ubuntu 24.04 cloud image, from
  `https://cloud-images.ubuntu.com/releases/noble/release/`.
- `SHA256SUMS` verified against `SHA256SUMS.gpg` in a throwaway `GNUPGHOME`.
  Good signature from "UEC Image Automatic Signing Key <cdimage@ubuntu.com>",
  fingerprint `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`.
- `ubuntu-24.04-server-cloudimg-amd64.squashfs` (263.5 MB), SHA-256
  `00b9709d33192c8cfb987fd9522e9a035c73fd125c5a479b125b5d26ffafec67`, matched
  the signed list.

**Findings**

1. **The CI kernel 6.1.155 supports squashfs and ext4 out of the box,** and
   Firecracker drives appear as `/dev/vda`, `/dev/vdb` and so on, in the drive
   order of the config.
2. **Ownership and setuid survive when read inside the guest.** `/usr/bin/sudo`
   shows as uid 0, mode 4755, and `/etc/shadow` as 0:42, mode 640. This confirms
   the review's point: assemble disks inside a VM as root, never by unpacking on
   the host as a normal user.
3. **Booting the squashfs as the root disk works.** `is_root_device: true` plus
   `is_read_only: true` makes Firecracker add `root=/dev/vda ro` itself. With
   `rootfstype=squashfs init=/bin/sh`, the kernel mounts it read-only and runs
   the shell as uid 0, and writes to `/` fail with "Read-only file system".
4. **Firecracker adds `pci=off` and the `virtio_mmio.device=` arguments
   itself;** the config's boot arguments must not repeat them.
5. **Node does not come with the image** (expected). The image reports
   `Ubuntu 24.04.5 LTS` and holds about 263 MB.
6. **Proposed change: write the guest init in Go.** A static Go binary worked as
   PID 1 straight away (the S1 and S2 spike init). Writing `cc-init` as a static
   Go program (`cmd/cc-guestinit`) instead of POSIX sh brings four gains:
   - unit tests for mounts, settings parsing and the final reboot
   - no reliance on the shell or utilities in the base image
   - one binary that serves both the builder and review VMs, picking its mode
     from the config device
   - the reboot syscall straight from code (S1 finding 4)

   `cc-claude` and `cc-selftest` can stay as small scripts, or move into the
   same binary. This needs the maintainer's approval as a spec change (spec §6,
   plan M8).

## S3: Claude Code install (2026-11-19)

**Setup.**

- A lockfile for `@anthropic-ai/claude-code@2.1.327`, then
  `npm ci --ignore-scripts` in a throwaway folder on the host.
- Not run inside a VM: that would need root for a tap device, and the questions
  S3 answers do not depend on where npm runs.
- The guest tools were checked inside the S2 inspect VM.

**Findings**

1. **Claude Code 2.1.327 comes as a native binary,** not a Node program. The
   main package (`hasInstallScript: true`) only pulls one optional platform
   package per OS, CPU and libc. Its `install.cjs` (402 lines) does nothing but
   hard-link or copy the platform binary over a placeholder `bin/claude.exe`: no
   network access and no other files.
2. **With `--ignore-scripts`, the placeholder refuses to run** ("claude native
   binary not installed"), but the platform binary itself runs on its own:
   - `node_modules/@anthropic-ai/claude-code-linux-x64/claude --version` prints
     `2.1.327 (Claude Code)`.
   - The binary: ELF x86-64, 355.6 MB, needing only glibc (`libc`, `libm`,
     `librt`, `libpthread`, `libdl`).
   - SHA-256 `343c744baa14bfd38286a7cb90871051f04379956da6c5b803a9e66019a76ac9`.
   - Its npm tarball:
     `https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-2.1.327.tgz`,
     integrity
     `sha512-uJ24I+t47cpQjeo31ZrHepsFQ8QUsLjCxmDxLshMjYHEtoxhQdgKc4iuViQDaKpFqn3puR85gwHlLj4e7s+1iQ==`.
3. **The Ubuntu 24.04 cloud image already holds everything else the guest
   needs:** glibc, `git`, `ip`, `bash`, `setpriv`, `runuser`, `timeout`,
   `useradd`, `update-ca-certificates` and `/etc/ssl/certs/ca-certificates.crt`.
   Only `rg` is missing. The binary carries a `USE_BUILTIN_RIPGREP` switch,
   which points to a bundled ripgrep; confirm by behaviour during S4 or the
   canary.
4. **The binary handles `HTTPS_PROXY` and `NODE_EXTRA_CA_CERTS` itself;** both
   strings appear in it. S4 checks the behaviour.

**Proposed change: an offline guest build.** Findings 1–3 remove every reason
for the builder VM to reach the network:

- The **host** downloads one npm tarball for the pinned version (HTTPS), checks
  its `sha512` integrity, extracts `package/claude` and checks the binary's
  SHA-256 pin. It never runs npm or Node, and runs no package scripts.
- The **builder VM** runs with **no network device at all**. It copies the
  squashfs tree onto a blank ext4 disk as root, then adds the `claude` binary,
  the plugin, the guest init, the sandbox CA and the `agent` user.

That drops these pieces from the design, together with their attack surface:

- `cc-tap1` and its nft rules
- the build proxy mode and its `snapshot.ubuntu.com` and `registry.npmjs.org`
  allowlist
- apt over HTTP
- `npm ci`, the lockfile and Node in the guest

It changes spec §6 and plan M5, M7 (build mode goes), M8 and M10, and needs the
maintainer's approval.

## S4: TLS interception (2026-11-19)

**Setup.**

- A Go CONNECT proxy (`~/spike/s4/main.go`, about 200 lines) on
  `127.0.0.1:3180`. It allows only `api.anthropic.com:443`, terminates TLS with
  a leaf from a fresh ECDSA CA carrying a name constraint for
  `api.anthropic.com`, and swaps `x-api-key` for the real key when the request
  carries the placeholder `sk-ant-sandbox-placeholder`. It logs header names and
  a placeholder flag, never key values.
- The S3 native binary runs under `env -i` with the placeholder key,
  `HTTPS_PROXY`, `NODE_EXTRA_CA_CERTS`,
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, a fresh `HOME`, and a `PATH`
  holding only `sh`, `bash`, `env`, `ls`, `cat`, `git` and `uname` (no `grep`,
  no `rg`).
- Part 1 used a dummy key; part 2 used the maintainer's real key from a
  dedicated, spend-limited workspace, entered at a hidden prompt and passed only
  to the proxy process.

**Findings**

1. **The proxy refuses other hosts:** a CONNECT to `example.com:443` got 403.
2. **The binary honours `HTTPS_PROXY` and trusts `NODE_EXTRA_CA_CERTS`,
   name-constrained CA included:** every connection went through CONNECT and the
   TLS handshake with the intercepting leaf succeeded (3 of 3).
3. **The key travels only in `x-api-key`;** no request carried `authorization`.
   Every request carried the placeholder, and the proxy swapped it every time (4
   of 4 in the final run).
4. **Requests seen:** `GET /api/claude_code/settings` (404),
   `GET /api/claude_code/policy_limits` (200, then 304), and `POST /v1/messages`
   (200). The review allowlist must keep the two `/api/claude_code/` paths
   working, or Claude Code may stall; M7 tests should cover them.
5. **End to end with the real key:** `success`, cost US$0.0089, answer `a.txt`.
6. **`--bare` removes the search tools.** The first real-key run offered no Grep
   tool; the model fell back to `grep` through Bash (which ran, since the spike
   omitted `--restricted`, and failed with exit 127). An offline probe (dummy
   key, API pointed at a dead local port) read the `init` event's tool list:

   | Flags                                                    | Tools offered                              |
   | -------------------------------------------------------- | ------------------------------------------ |
   | `--bare`                                                 | Bash, Edit, Read                           |
   | `--bare --restricted`                                    | Edit, Read                                 |
   | `--bare --restricted --tools Read,Grep,Glob,Agent,Skill` | Read                                       |
   | `--restricted`                                           | 19 tools, including Edit, Write, WebSearch |
   | `--restricted --tools Read,Grep,Glob,Agent,Skill`        | Task, Glob, Grep, Read, Skill              |

   Neither `USE_BUILTIN_RIPGREP`, `CLAUDE_CODE_USE_NATIVE_FILE_SEARCH` nor
   `EMBEDDED_SEARCH_TOOLS=0` brings Grep back under `--bare`. With
   `--plugin-dir`, the `codecheck:codecheck` and `codecheck:finding-authoring`
   skills load either way.

7. **The bundled search works with no `rg` installed:** rerun with
   `--restricted --strict-mcp-config --tools Read,Grep,Glob`, the session
   offered `Glob`, `Grep` and `Read`, called `Grep` with pattern `SPIKE`, and
   got `Found 1 file a.txt`. So the guest image needs no `rg` (closes the S3
   open point).

**Proposed change: drop `--bare`.** The guest runs
`claude -p --restricted --strict-mcp-config --tools Read,Grep,Glob,Agent,Skill --allowedTools Read,Grep,Glob,Agent,Skill --plugin-dir … --permission-mode dontAsk …`.
`--restricted` still ignores user, project and local settings and removes the
command-running tools and WebFetch; `--tools` fixes the exact tool set. Built-in
skills still load (such as `deep-research` and `batch`), but with only these
tools they can only read. The guest `HOME` starts empty on every run, so no
settings or hooks exist for `--bare` to skip anyway. This changes spec §6, the
deployment plan's Phase 3 flags and plan M8 (`cc-claude`), and the M8 grep test
must check for `--tools` and the absence of `--bare`. Needs the maintainer's
approval.

## S5: sudo handover (2026-11-19)

**Setup.** A Bubble Tea v1.3.10 program (`~/spike/s5/main.go`) on the alt
screen. Its keys:

- `s` hands the terminal to `sudo -v` through `tea.ExecProcess`.
- `t` runs `sudo -n true` in the background, with stdin set to `/dev/null`.
- `k` runs `sudo -n -v`.

The maintainer ran it in their own terminal after `sudo -k`, under sudo
1.9.17p2. This host has no sudo-rs and no doas; it has `run0`, which goes
untested.

**Findings**

1. **Without a cached login, `sudo -n` fails at once** (exit 1, "a password is
   required") and never hangs or draws over the screen. The engine can use that
   as its "needs sudo" signal.
2. **`tea.ExecProcess(sudo -v)` works:** Bubble Tea leaves the alt screen, sudo
   prompts on the normal screen, and on success the program resumes and redraws
   (`✓ sudo -v succeeded; back in the TUI`).
3. **Background `sudo -n` then works while the program owns the screen:** both
   `sudo -n true` and the keep-alive `sudo -n -v` succeeded.
4. **The prompt leaves a trace:** the `[sudo] password for …:` line stays on the
   normal screen and shows in the scrollback after the program exits. That looks
   only cosmetic. The wizard can print a one-line explanation before the
   handover, so the prompt reads in context.
5. **This host caches sudo for 10 minutes** (`timestamp_timeout=10`). A
   60-second keep-alive sits well inside that. The default `timestamp_type` ties
   the cache to the terminal, which works because the wizard's children share
   it. The spec's assumption holds as written.

No spec change. M2 `privilege.go` keeps sudo-rs, doas and `run0` support behind
the same `-n` contract; `run0` authenticates through polkit, which may prompt
for each root command unless its policy caches; the wizard should detect that
and warn instead of assuming a cache.
