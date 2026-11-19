# Uninstall

> **Planned.** `codecheck-sandbox uninstall` currently reports "not
> implemented".

```sh
codecheck-sandbox uninstall          # keeps your reports
codecheck-sandbox uninstall --purge  # also deletes reports and caches
```

Setup records everything it creates in a root-owned manifest. `uninstall` lists
what it will remove, asks you to confirm, and then reverses each step in the
opposite order:

- the proxy service, the tap device and the firewall table;
- `cc-launch` and its sudoers rule;
- the guest image, the kernel, Firecracker and the jailer files;
- the `fcjail` user;
- firewalld, ufw, NetworkManager and SELinux adjustments;
- the stored API key and the sandbox CA.

It leaves alone anything that existed before setup, such as packages or users
you already had; the manifest marks those "adopted". If the manifest has gone
missing, `uninstall` scans the known locations instead.

Afterwards, `codecheck-sandbox doctor` should report nothing installed. Remove
the `codecheck-sandbox` binary itself with
`rm "$(command -v codecheck-sandbox)"`.

Your Anthropic API key still works after uninstalling; revoke it in the
Anthropic Console if you no longer need it.
