# Troubleshooting

> **Planned.** This page grows as each part lands, with one section per setup
> step and the fix for each known failure.

## First steps

1. Run `codecheck-sandbox doctor`. It checks every part of the setup and names
   the first thing that fails.
2. Re-run `codecheck-sandbox setup`. It skips what already works and retries the
   rest.
3. Re-run the failing command with `--debug --plain` to see each command and its
   result.

## Getting help

- Logs live in `~/.local/state/codecheck-sandbox/log/`, with the API key
  redacted.
- `codecheck-sandbox doctor --bundle` packs recent logs, the `doctor` output and
  version details into one archive for a bug report. It lists the files before
  writing anything, removes anything shaped like an API key and your home
  folder's path, and never overwrites a file (`--out FILE` picks the name).
  Check the archive yourself before sharing it.
- Open a
  [bug report](https://github.com/ModrichLabs/codecheck-sandbox/issues/new/choose).
  Security problems go through the
  [private reporting form](https://github.com/ModrichLabs/codecheck-sandbox/security/advisories/new)
  instead.

## Known situations

### `/dev/kvm` missing

Enable virtualisation (VT-x or AMD-V) in your firmware settings. In a VM or
cloud instance, enable nested virtualisation.

### `cc-launch` refuses to boot

`cc-launch` checks the images, the firewall rules and the proxy before every
boot, and refuses on any mismatch. A common cause: something reloaded
`nftables.service` and flushed the sandbox's table. Run
`codecheck-sandbox setup` to restore it, then `doctor` to confirm.
