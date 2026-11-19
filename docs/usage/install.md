# Install

> **Planned.** No release exists yet. Until one does, you can build from source
> to try the parts that exist.

## From source

You need Go 1.27 or newer and git.

```sh
git clone https://github.com/ModrichLabs/codecheck-sandbox.git
cd codecheck-sandbox
go install ./cmd/codecheck-sandbox
codecheck-sandbox version
```

`go install` puts the binary in `$(go env GOPATH)/bin`; add that folder to your
`PATH` if your shell can't find the command.

Setup installs the other two binaries, `cc-launch` and `cc-guestinit`, itself:
`cc-launch` into `/usr/local/lib/codecheck-sandbox/bin/`, owned by root, and
`cc-guestinit` into the guest image.

## Release binaries

Planned for a later release, with checksums and signatures.

## Next

Run the [setup wizard](setup.md).
