# Requirements

`codecheck-sandbox setup` checks each of these and tells you how to fix anything
missing.

## Machine

| Need                          | Why                                                            | How to check                                     |
| ----------------------------- | -------------------------------------------------------------- | ------------------------------------------------ |
| Linux on x86_64               | This project targets x86_64 first; aarch64 may follow          | `uname -m` prints `x86_64`                       |
| KVM                           | Firecracker needs hardware virtualisation                      | `/dev/kvm` exists; enable VT-x/AMD-V in firmware |
| systemd and cgroups v2        | VM lifetime, the proxy service, the tap device                 | `/sys/fs/cgroup/cgroup.controllers` exists       |
| 8 GB RAM or more              | The host plus a 2–4 GB VM                                      | `free -g`                                        |
| 10 GB free disk on `/var/lib` | Base image, guest image and per-run disks                      | `df -h /var/lib`                                 |
| sudo, sudo-rs, doas or run0   | Setup installs packages, a network device and a firewall table | `sudo -v`                                        |

Virtual machines and cloud instances work when they offer nested virtualisation,
so `/dev/kvm` appears inside them.

## Packages

Setup installs these through your distribution's package manager when missing:
`nftables`, `e2fsprogs`, `squashfs-tools`, `git`, `iproute2` and `gnupg`. It
knows the package names for Arch, Debian and Ubuntu, Fedora and RHEL, and
openSUSE. On other distributions it lists the packages and waits while you
install them.

## Things setup works alongside

- **firewalld or ufw:** setup adds one allowance for the proxy port on the
  sandbox's tap device.
- **NetworkManager:** setup tells it to leave the sandbox's tap devices alone.
- **Docker:** setup works next to it, but warns you that membership of the
  `docker` group amounts to root access.
- **SELinux or AppArmor:** setup detects the mode and applies labels where
  needed.

## Anthropic account

- An **API key** ("Somewhere else (use an API key)" in the Anthropic Console).
  Sign-in and identity-federation options don't work with the proxy.
- Create the key inside a **dedicated workspace** with a **monthly spend
  limit**. A misused key then can't spend beyond that limit or touch anything
  else in your account.
- Each review also takes a per-run budget (US$25 by default).

## Permission to review the code

Reviews send the code under review to Anthropic's API. Only review code you have
the right to share that way.
