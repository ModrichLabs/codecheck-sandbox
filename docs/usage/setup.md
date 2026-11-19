# Setup

> **Planned.** `codecheck-sandbox setup` currently reports "not implemented".
> This page describes the design; it becomes step-by-step instructions as the
> wizard lands.

```sh
codecheck-sandbox setup
```

The wizard runs full-screen in your terminal. A sidebar shows the eight phases,
and the main panel shows each step's status. Re-running `setup` skips every step
that already passes its check, so you can stop and resume at any time.

## Root steps: show, confirm, run

Before any step that needs root, the wizard shows the exact commands and file
contents. Press:

- **Enter** to run it,
- **d** to read the full files,
- **s** to skip it,
- **q** to quit.

The first time a step needs root, the wizard hands the terminal to `sudo` for
your password. After that it keeps the sudo login fresh in the background, and
never asks for or stores your password itself.

## The phases

1. **Welcome.** What setup will change, and a reminder that reviews send code to
   Anthropic.
2. **Platform checks.** CPU, KVM, systemd, cgroups, memory, disk, privilege
   tool, firewall managers, and SELinux or AppArmor. See
   [requirements](requirements.md).
3. **Components.** Detects your distribution and installs missing packages.
4. **Your details.**
   - Your Anthropic API key, typed into a hidden field and tested once. Setup
     stores it in a root-only file that only the proxy service can read.
   - The default per-review budget (US$25).
   - Where reports go.
   - The VM size, scaled to your machine.
   - The CodeCheck version to use.
   - Whether to send anonymous usage and error reports (off unless you choose
     it; see [privacy and telemetry](privacy-and-telemetry.md)).
5. **Host setup.** The jail user, the tap device, the firewall table, the proxy
   service, `cc-launch` and its sudoers rule, plus any firewalld, ufw,
   NetworkManager or SELinux adjustments.
6. **Guest build.** Downloads and verifies the inputs, then builds the guest
   image inside a Firecracker VM with no network. Takes a few minutes.
7. **Verify.** Runs `selftest`, which boots a VM and tries each way out of the
   sandbox, and offers an optional paid `canary` review (about US$1–5).
8. **Summary.** What got installed, and what to run next.

## Unattended setup

```sh
CODECHECK_API_KEY=... codecheck-sandbox setup --yes
```

`--yes` runs every root step without asking. It reads the API key from the
`CODECHECK_API_KEY` environment variable (never from a flag), and the other
answers from flags or `~/.config/codecheck-sandbox/config.toml`. It stops if an
answer goes missing. Telemetry stays off under `--yes` unless the config file
turns it on.

`--dry-run` prints every planned command and file, and changes nothing.
