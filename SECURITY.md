# Security policy

codecheck-sandbox exists to contain an AI agent that reads untrusted code, so we
treat any way around that containment as a serious bug.

## Reporting a vulnerability

Report privately through GitHub:
[open a private security advisory](https://github.com/ModrichLabs/codecheck-sandbox/security/advisories/new).
Please don't open a public issue, discussion or pull request for a
vulnerability.

Include what you can of:

- what an attacker could do, and from where (code under review, the guest VM, a
  local user, the network);
- steps or a proof of concept to reproduce it;
- the codecheck-sandbox version (`codecheck-sandbox version`), Linux
  distribution and kernel version;
- any logs, with secrets removed. `codecheck-sandbox doctor --bundle` redacts
  the API key, but please check the bundle before sharing it.

We aim to acknowledge a report within five business days, agree on a fix and a
disclosure date with you, and credit you in the advisory unless you'd rather
stay anonymous.

## Supported versions

The project has not reached 1.0. Only the latest `0.y.z` release, and `main`,
get security fixes.

| Version        | Supported |
| -------------- | --------- |
| latest `0.y.z` | Yes       |
| older `0.y.z`  | No        |

## Scope

In scope, among others:

- code under review, or the agent in the VM, reaching anything beyond the
  Anthropic API through the proxy: the host, the local network, other hosts on
  the internet, or other host ports;
- the real API key reaching the guest, a log, a command line, or a telemetry
  event;
- a way to make `cc-launch` (which runs as root) boot an unverified image, skip
  its checks, or act on a path it should refuse;
- telemetry sending anything beyond the documented event fields;
- an escape from the Firecracker jailer chroot attributable to how this project
  configures it.

Out of scope here (report them upstream instead):

- vulnerabilities in Firecracker itself:
  [Firecracker's security policy](https://github.com/firecracker-microvm/firecracker/security/policy);
- vulnerabilities in CodeCheck's prompts or findings:
  [CodeCheck](https://github.com/Commonwealth-Bank-of-Australia/CodeCheck);
- vulnerabilities in Claude Code: Anthropic's
  [responsible disclosure policy](https://www.anthropic.com/responsible-disclosure-policy).

## Threat model in brief

[docs/architecture.md](docs/architecture.md) describes the design. In short:
each review runs in a fresh Firecracker microVM under the jailer, with a
read-only copy of the code, no route to anything except a proxy on the host, and
only a placeholder API key. The proxy allows HTTPS to `api.anthropic.com` alone
and swaps in the real key outside the VM. A root-owned helper re-checks the
image hashes and firewall rules before every boot.
