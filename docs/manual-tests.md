# Manual tests

Unit tests can't drive a real password prompt or a real terminal. Run these
checks by hand before a release, on a machine where the privilege tool asks for
a password, and note the date, tool and terminal in the pull request.

## Privilege handover (`internal/tui/sudo.go`)

Start from a cold login: run `sudo -k` (or `doas -L`) first.

1. Run `codecheck-sandbox setup` and approve the first root step.
2. **The prompt:** the interface gives way to the terminal, which shows
   `codecheck-sandbox setup needs root for the next step. Password for <you>:`
   (sudo and sudo-rs) or the tool's own prompt (doas, run0).
3. **Typing:** the password doesn't echo; Backspace works.
4. **Return:** after a good password, the interface comes back in full, and the
   step runs.
5. **A wrong password three times:** the interface comes back with the step
   failed, the failure screen explains it, and `r` asks again.
6. **Ctrl-C at the prompt:** the interface comes back, the step fails, and
   nothing ran as root.
7. **No second prompt:** later root steps run without asking, even past the
   tool's timeout (sudo's default lasts 5 minutes; the keep-alive refreshes
   every 60 seconds). To test the lapse, set `Defaults timestamp_timeout=1` in a
   sudoers drop-in, pause at a confirm panel for over a minute with the
   keep-alive stopped (`sudo -k` from another terminal), then approve: the
   handover runs again and the step carries on.
8. **Quitting:** `q` or Ctrl-C during a root step asks first; `y` quits, and
   `ps` then shows no leftover `sudo -n -v`.

Check with sudo, sudo-rs and doas where available; run0 asks polkit, so check
that its agent prompt appears in the terminal.
