# Canary sample

`codecheck-sandbox canary` reviews this folder to check that a whole review
works inside the sandbox. `app.py` holds a deliberate SQL injection (CWE-89),
which the review should report. Never deploy it.
