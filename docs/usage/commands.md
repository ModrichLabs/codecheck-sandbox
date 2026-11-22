# Commands

> **Status:** every command below exists, but only `version` works yet. The rest
> report "not implemented" and describe the planned behaviour here.

## Global flags

| Flag             | Effect                                                                   |
| ---------------- | ------------------------------------------------------------------------ |
| `--yes`          | Run root steps without asking; read the API key from `CODECHECK_API_KEY` |
| `--dry-run`      | Print what would change, and change nothing                              |
| `--plain`        | Print plain log lines instead of the full-screen interface               |
| `--debug`        | Log more detail, and echo the log to the terminal                        |
| `--no-telemetry` | Send no usage or error reports, whatever the settings say                |
| `--version`      | Print the version                                                        |
| `-h`, `--help`   | Show help for any command                                                |

The `--yes`, `--dry-run`, `--plain`, `--debug` and `--no-telemetry` flags
already parse, but no command acts on them yet.

## Commands

| Command     | Status  | What it does                                                                                     |
| ----------- | ------- | ------------------------------------------------------------------------------------------------ |
| `version`   | Works   | Prints `codecheck-sandbox <version>`                                                             |
| `setup`     | Planned | The guided setup wizard; see [setup](setup.md)                                                   |
| `doctor`    | Planned | Runs every check, the image and firewall hashes and a config-drift check; exits 1 on any failure |
| `selftest`  | Works   | Boots a VM and tries each way out of the sandbox; every probe must fail to get out (see below)   |
| `canary`    | Works   | Reviews a small, known-vulnerable sample (budget US$5) to confirm reviews work end to end        |
| `review`    | Works   | Reviews a repository inside a fresh VM (see below)                                               |
| `rebuild`   | Planned | Rebuilds the guest image, for example `rebuild --commit <sha>` for another CodeCheck commit      |
| `uninstall` | Planned | Removes everything setup installed; see [uninstall](uninstall.md)                                |
| `telemetry` | Works   | `telemetry on`, `off` or `status`; see [privacy and telemetry](privacy-and-telemetry.md)         |

A hidden `proxy` command also exists. The `codecheck-proxy` systemd service runs
it; you never need to run it yourself.

## Checking the sandbox

- **`selftest`** boots a VM that tries every way out: DNS, other host ports,
  IPv6 link-local, a spoofed source address, a Docker-published port, and every
  port the host listens on where the VM could reach it (on any address, or on
  the tap's). `--port 5432` adds more (up to 15 in all). It passes, and exits 0,
  only when every probe gets stopped.
- **`canary`** reviews a tiny sample with a planted SQL injection, with a US$5
  budget (it usually costs US$1-5). It passes when the review ends cleanly,
  reports CWE-89, and the proxy refused nothing, which shows Claude Code needs
  no host beyond the allowlist.

## Reviews

```sh
codecheck-sandbox review <path|url> [--pr N | --base REF] [--prompt TEXT] [--cwe ID] [--budget USD]
```

- **Full review:** `codecheck-sandbox review https://github.com/org/repo`
- **Pull request:** `--pr 42` fetches the pull request's head and reviews the
  change against its base.
- **Diff against a ref:** `--base main` reviews only what changed since that
  ref.
- **Focus on a weakness:** `--cwe 89` (or `--cwe CWE-89`) has CodeCheck hunt
  that CWE family first. It sets a priority, not a limit: every other class
  still gets reviewed. Repeat it for up to ten CWEs.
- **Extra instructions:**
  `--prompt "Review ./routes; treat the admin API as internet-facing"` adds one
  line to the request, such as a sub-folder, a threat model, or a deliberate
  narrowing (CodeCheck then lists what it left out under Limitations). It takes
  up to 1,000 bytes of plain text on one line; anything else, such as a line
  break, gets refused rather than trimmed.
- **Budget:** `--budget 10` stops the review at US$10. The default comes from
  `budget_usd` in `config.toml` (US$25 unless setup changed it), and the most a
  review may take is US$1,000.

The tool clones a fresh copy on the host, strips git hooks and configuration
from it, and gives the VM a read-only copy. While the review runs, the screen
shows elapsed time and the VM's state. Afterwards you get `report.md` and a
`summary.json` with cost, duration and any warnings, such as proxy denials or
signs that the model refused.

`review` exits 0 only when the review ends cleanly. It exits 1, after writing
the report, when the review stopped at its budget, read like a refusal, hit an
error or never finished, so scripts can tell.
