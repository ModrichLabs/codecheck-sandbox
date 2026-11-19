# Privacy and telemetry

> **Planned.** No telemetry exists in the code yet. This page records the
> commitments the design makes; the list of events becomes exact once the
> telemetry code lands.

## What always leaves your machine

- **The code under review goes to Anthropic.** CodeCheck sends it to Anthropic's
  API for analysis, under your API key and Anthropic's terms. Only review code
  you have the right to share that way.
- **Downloads during setup.** Setup fetches pinned, checksummed files from
  GitHub, Ubuntu's image server, npm's registry (one Claude Code package
  tarball) and your distribution's package mirrors.

Nothing else leaves the machine unless you turn on telemetry.

## What stays local

- **Logs** in `~/.local/state/codecheck-sandbox/log/`, with your API key
  redacted, rotated at 10 MB with five files kept.
- **Reports** and a `summary.json` per review, in the reports folder you choose.
- **Diagnostic bundles** that `doctor --bundle` writes, which you share only if
  you choose.

## Optional telemetry

If you agree during setup, the CLI sends anonymous usage and error reports to
the project's PostHog instance (PostHog Cloud, US). It helps the project see
which distributions fail at which step, how reviews perform, and whether the
containment holds in practice.

- **Off unless you say yes.** `DO_NOT_TRACK=1`, `--no-telemetry`, or unattended
  runs without an explicit setting all keep it off.
- **Change your mind any time:** `codecheck-sandbox telemetry off` (which also
  deletes your install id and any unsent events), `telemetry on`, or
  `telemetry status`.
- **Anonymous.** A random install id identifies the installation. PostHog
  creates no person profile, works out no location, and the project discards IP
  addresses.
- **Sent only by the CLI.** The sandbox VM has no route to PostHog, and the root
  helper `cc-launch` makes no network calls.
- **Never slows you down.** Events go in one batch at the end of a command, with
  a 3-second limit, and get dropped quietly on failure.

### What a report may contain

- **Usage:** the command run, the setup phase reached, each step's outcome, the
  review mode (full, diff or pull request), duration and cost as ranges (for
  example "US$1–5"), and the versions of codecheck-sandbox and Claude Code.
- **Errors:** the kind of error, the step it happened in, and a stack trace of
  this project's own function names and file paths, never the error message.
- **Environment:** distribution family and version, CPU architecture, kernel
  version, RAM and disk as ranges, firewall manager, SELinux or AppArmor mode,
  and the privilege tool.
- **Security signals, as counts:** proxy denials per review, `selftest` probes
  that failed, and `cc-launch` refusals by reason.

### What a report never contains

Code, file contents, file paths outside this project, repository names or URLs,
pull request numbers, prompts, findings, error messages, your API key,
hostnames, usernames, IP addresses, or exact costs and durations. A test in the
code base checks this on every build.
