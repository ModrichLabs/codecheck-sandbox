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

## Commands

| Command     | Status  | What it does                                                                                     |
| ----------- | ------- | ------------------------------------------------------------------------------------------------ |
| `version`   | Works   | Prints `codecheck-sandbox <version>`                                                             |
| `setup`     | Planned | The guided setup wizard; see [setup](setup.md)                                                   |
| `doctor`    | Planned | Runs every check, the image and firewall hashes and a config-drift check; exits 1 on any failure |
| `selftest`  | Planned | Boots a VM and tries each way out of the sandbox; every probe must fail to get out               |
| `canary`    | Planned | Reviews a small, known-vulnerable sample (budget US$5) to confirm reviews work end to end        |
| `review`    | Planned | Reviews a repository inside a fresh VM (see below)                                               |
| `rebuild`   | Planned | Rebuilds the guest image, for example `rebuild --commit <sha>` for another CodeCheck commit      |
| `uninstall` | Planned | Removes everything setup installed; see [uninstall](uninstall.md)                                |
| `telemetry` | Planned | `telemetry on`, `off` or `status`; see [privacy and telemetry](privacy-and-telemetry.md)         |

## Reviews

```sh
codecheck-sandbox review <path|url> [--pr N | --base REF] [--prompt TEXT] [--cwe ID] [--budget USD]
```

- **Full review:** `codecheck-sandbox review https://github.com/org/repo`
- **Pull request:** `--pr 42` fetches the pull request's head and reviews the
  change against its base.
- **Diff against a ref:** `--base main` reviews only what changed since that
  ref.
- **Focus:** `--prompt "Review ./routes for injection flaws"`, or `--cwe 89`.
- **Budget:** `--budget 10` stops the review at US$10 (default US$25).

The tool clones a fresh copy on the host, strips git hooks and configuration
from it, and gives the VM a read-only copy. While the review runs, the screen
shows elapsed time and the VM's state. Afterwards you get `report.md` and a
`summary.json` with cost, duration and any warnings, such as proxy denials or
signs that the model refused.
