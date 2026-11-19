# Architecture

This page explains how codecheck-sandbox works and why its containment holds. It
summarises the [design spec](specs/2026-11-19-codecheck-sandbox-wizard.md),
which holds the full detail and the reasoning behind each decision.

> **Status:** design approved; implementation in progress. Sections describe the
> intended design until the matching milestone lands, and the
> [plan](plans/2026-11-19-codecheck-sandbox.md) tracks which parts exist.

## The problem

[CodeCheck](https://github.com/Commonwealth-Bank-of-Australia/CodeCheck) runs
Claude Code as a security reviewer over a codebase. The code under review counts
as untrusted input: comments, docs or test fixtures may carry prompt injection
that tries to make the agent run commands, read secrets or send data elsewhere.
CodeCheck's own limits live in its prompts, which an injection can talk around.
codecheck-sandbox moves the limits outside the model, into the operating system
and the network.

## Components

| Component                 | Runs as                        | Job                                                                                   |
| ------------------------- | ------------------------------ | ------------------------------------------------------------------------------------- |
| `codecheck-sandbox`       | You                            | The CLI and setup wizard: checks, setup steps, reviews, reports                       |
| `cc-launch`               | root, through one sudoers rule | Verifies the images and firewall, then boots one VM under the jailer                  |
| `codecheck-proxy.service` | A systemd `DynamicUser`        | The egress proxy: HTTPS to `api.anthropic.com` only, swapping in the real API key     |
| Firecracker and jailer    | `fcjail`, in a chroot          | The microVM monitor, with seccomp filters, cgroups and a PID namespace                |
| `cc-guestinit`            | PID 1 inside the VM            | Mounts disks, configures the network, runs the job, then reboots so Firecracker exits |
| `cc-claude`               | `agent` inside the VM          | Runs Claude Code with CodeCheck and a fixed, read-only tool set                       |

## A review, end to end

```mermaid
sequenceDiagram
    participant U as You
    participant CLI as codecheck-sandbox
    participant L as cc-launch (root)
    participant VM as Firecracker VM
    participant P as Proxy (host)
    participant A as api.anthropic.com

    U->>CLI: review <repo>
    CLI->>CLI: Fresh clone, harden .git, build code and config disks
    CLI->>L: sudo -n cc-launch <run-id>
    L->>L: Check lock, image hashes, firewall-rule hash, proxy owner
    L->>VM: Boot under jailer (read-only root, code, config; blank output disk)
    VM->>P: CONNECT api.anthropic.com:443 (placeholder key)
    P->>A: Same request, real key
    A-->>VM: Model responses (through the proxy)
    VM->>VM: Write transcript to the output disk, reboot
    L->>CLI: Copy capped files off the output disk
    CLI->>U: report.md, summary, post-run checks
```

## Trust boundaries

1. **Guest to host network.** The VM's only interface, the tap `cc-tap0`,
   reaches `172.16.0.1:3180` (the proxy) and nothing else. An nftables table
   drops every other packet from the tap and all forwarding. The VM has no
   default route, no DNS and no IPv6.
2. **Proxy to the internet.** The proxy accepts `CONNECT api.anthropic.com:443`
   from the guest address only. It terminates TLS with a sandbox CA limited by
   name constraints to `api.anthropic.com`, so the CA can't vouch for any other
   site even if stolen. It forwards only requests that carry the placeholder
   key.
3. **The API key.** The real key lives in a root-only file that systemd hands to
   the proxy service alone. The guest holds `sk-ant-sandbox-placeholder`. Code
   in the VM can't read the key, and can't swap in its own key to send data to
   another account, because the proxy refuses any other key.
4. **The VM itself.** Firecracker runs under the jailer: a chroot, an
   unprivileged user, seccomp filters, cgroups v2 and a new PID namespace,
   inside a `systemd-run` scope with a hard time limit.
5. **Root on the host.** Only `cc-launch` runs as root without a password. It
   takes a run id and nothing else, and before each boot it checks the kernel
   and root disk hashes, the firewall rules' hash, and that the real proxy
   service owns the proxy port. Any mismatch stops the run.
6. **Claude Code inside the VM.** It runs with `--restricted`, an exact
   `--tools Read,Grep,Glob,Agent,Skill` list, `--strict-mcp-config` and a spend
   cap. That layer helps, but the design never depends on it: the boundaries
   above hold even if the agent misbehaves.

## Setup and the step engine

Every piece of setup becomes a `Step` with four methods: `Check` (does it
already hold?), `Plan` (the exact commands and files), `Apply` and `Undo`. The
wizard walks the steps, `doctor` runs only the checks, `--dry-run` prints the
plans, and `uninstall` runs the undos in reverse. Every external command goes
through one `Runner` interface, so tests can fake the whole machine.

Root steps show their commands and file contents before running, unless you pass
`--yes`. The terminal hands over to `sudo -v` once for your password; after
that, root commands run with `sudo -n`, so nothing can prompt behind the
interface.

## The guest image

The host downloads and verifies every input: the Ubuntu 24.04 cloud image
(GPG-signed checksums), Firecracker's guest kernel, the Claude Code native
binary (npm integrity hash plus a binary hash), and CodeCheck at a pinned git
commit. A builder VM with **no network device** assembles the root disk as root
inside the VM, so file ownership comes out right without root on the host.

## Logging and telemetry

Local logs, with secrets redacted, always stay on the machine. Remote telemetry
stays off unless you opt in; when on, the CLI (never the VM, never `cc-launch`)
sends typed, allow-listed events to PostHog, with bands instead of exact values.
See [privacy and telemetry](usage/privacy-and-telemetry.md).

## Limits

- Linux on x86_64 with KVM only; aarch64 may come later.
- One VM at a time.
- Reviews send the code under review to Anthropic.
