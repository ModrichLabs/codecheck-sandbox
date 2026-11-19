# codecheck-sandbox

Run [CodeCheck](https://github.com/Commonwealth-Bank-of-Australia/CodeCheck)
security reviews inside jailed Firecracker microVMs, with a guided setup wizard.

> **Status: early development (pre-release, `0.1.0-dev`).** The command tree
> exists, but setup, reviews and the other commands don't work yet. Follow the
> [plan](docs/plans/2026-11-19-codecheck-sandbox.md) for progress. Don't rely on
> this project to contain anything until a release says so.

## Why

CodeCheck turns Claude Code into an LLM-driven static application security
tester. It reads a codebase and reports likely vulnerabilities. Its "read-only"
guardrails exist only as prompts, and the code it reads may try to manipulate
it. codecheck-sandbox adds containment from outside the model:

- **One fresh microVM per review.** Each review runs in a
  [Firecracker](https://firecracker-microvm.github.io/) microVM under the
  jailer, with a read-only copy of the code and a throwaway disk for output.
- **One way out.** The VM can reach only a proxy on the host, and the proxy
  allows HTTPS to `api.anthropic.com` alone.
- **No real key inside.** The VM holds a placeholder; the proxy swaps in your
  API key on the way out, so code in the VM can never read or exfiltrate it.
- **Checked before every boot.** A small root-owned helper re-checks the image
  hashes and firewall rules each time, and refuses to boot if anything has
  changed.

## What it will do

```text
codecheck-sandbox setup       # guided, step-by-step setup of this machine
codecheck-sandbox doctor      # check the setup and report problems
codecheck-sandbox selftest    # boot a VM and try each way out of the sandbox
codecheck-sandbox review <repo|url> [--pr N | --base REF] [--budget USD]
codecheck-sandbox uninstall   # remove everything setup installed
```

The setup wizard checks the machine, installs missing packages through your
distribution's package manager, shows every root command before it runs it, and
builds the guest image inside a Firecracker VM with no network access.

## Requirements

- Linux on x86_64, with KVM (`/dev/kvm`), systemd and cgroups v2.
- At least 8 GB of RAM and 10 GB of free disk.
- sudo (or sudo-rs, doas or run0).
- An Anthropic API key, ideally from a dedicated workspace with a spend limit.
- Permission to send the code you review to Anthropic.

See [docs/usage/requirements.md](docs/usage/requirements.md) for detail.

## Documentation

| Document                                                            | For                                        |
| ------------------------------------------------------------------- | ------------------------------------------ |
| [Usage guide](docs/usage/README.md)                                 | Installing, setting up and running reviews |
| [Architecture](docs/architecture.md)                                | How the sandbox works, and why it holds    |
| [Design spec](docs/specs/2026-11-19-codecheck-sandbox-wizard.md)    | The approved design and its amendments     |
| [Plan](docs/plans/2026-11-19-codecheck-sandbox.md)                  | Implementation tasks and progress          |
| [Spike findings](docs/plans/2026-11-19-codecheck-sandbox-spikes.md) | What the early experiments showed          |
| [Contributing](CONTRIBUTING.md)                                     | How to contribute                          |
| [Security policy](SECURITY.md)                                      | How to report a vulnerability              |
| [Changelog](CHANGELOG.md)                                           | What changed in each version               |

## Privacy

Reviews send the code under review to Anthropic's API, since CodeCheck works
that way. Nothing else leaves the machine unless you opt in to anonymous usage
and error reporting during setup, and even then only fixed, documented fields
with no code, paths, repository names or keys. See
[docs/usage/privacy-and-telemetry.md](docs/usage/privacy-and-telemetry.md).

## Licence

Copyright 2026 Modrich Labs. Licensed under the [Apache License 2.0](LICENSE).

CodeCheck belongs to the Commonwealth Bank of Australia and uses the MIT
licence. This project fetches it as a pinned dependency and has no affiliation
with the Commonwealth Bank of Australia. See [NOTICE](NOTICE).
