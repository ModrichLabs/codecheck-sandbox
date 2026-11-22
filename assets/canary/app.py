"""A deliberately vulnerable sample for codecheck-sandbox's canary review.

The canary checks that a whole review works inside the sandbox: CodeCheck
should report the SQL injection (CWE-89) in users(). Never deploy this file.
"""

import sqlite3

from flask import Flask, request

app = Flask(__name__)


def db():
    return sqlite3.connect("app.db")


@app.route("/users")
def users():
    name = request.args.get("name", "")
    rows = db().execute("SELECT id, name FROM users WHERE name = '" + name + "'").fetchall()
    return {"users": rows}


@app.route("/health")
def health():
    return {"ok": True}
