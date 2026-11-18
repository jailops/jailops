# ipv6-switched-cloud

A cloud server whose provider gives you **a `/64` on a segment shared with other customers**. The provider's router
believes every address of the `/64` is on-link and asks neighbor discovery who has it, so JailOps answers for each jail
address itself (**the NDP proxy**, see [networking-ipv6.md](../../docs/networking-ipv6.md)). Two projects, each with its own
`/80` carved out of the `/64`:

| Project | Network | IPv6 `/80` | IPv4 | Jails |
|---|---|---|---|---|
| `shop` | `shop` | `2001:db8:1:2:100::/80` | `10.94.0.0/24` (NAT) | `web` (public), `app` |
| `intra` | `intra` | `2001:db8:1:2:101::/80` | `10.95.0.0/24` (NAT) | `wiki` |

Replace `2001:db8:1:2::/64` with your provider's prefix and `vtnet0` with the uplink. Keep the host's own address outside
the carved `/80`s.

## What the example shows

- **Networks with the NDP proxy** (`ipv6.ndp_proxy: true`, gateway `fe80::1`, uplink `vtnet0`, a range inside the `/80`). A network
  belongs to one project; the two `/80`s cannot overlap (`validate` checks that offline).
- **The floor**: both projects name `firewall_policies: [floor]`, a policy with `default_in: deny`. A jail's own policies add rules but cannot
  weaken it. Projects cannot reach each other: there is no rule that says they can.
- **A firewall per jail**: `web` has `public-web` (HTTP and HTTPS from anywhere, SSH from `set:admins`), `app` has
  `app-private` (port 8080 only from `set:web-tier`).
- **A public web jail** with a fixed IPv6 address (`2001:db8:1:2:100::80`, your AAAA record) and `expose:` for ports 80 and 443: over IPv6
  the jail's own address answers (no NAT), over IPv4 the host's ports are forwarded.
- Explicit network and storage on every jail; the image is built once by `build: true`.

## Before you apply

1. Nothing to write in `pf.conf` for the jails: the `allow in` rules of `public-web` open the uplink for the
   jail's own addresses. Delete any `pass in quick on vtnet0 ... to <jail prefix>` rule you added earlier.
2. Run `jailops doctor`: it fails `network.ndproxy` while a proxy network is not answering.

## Run it

    jailops-sync -C examples/ipv6-switched-cloud validate
    jailops-sync -C examples/ipv6-switched-cloud plan       # compare with EXPECTED-PLAN.md
    jailops-sync -C examples/ipv6-switched-cloud sync --apply
    jailops-sync -C examples/ipv6-switched-cloud drift      # later: non-zero when something changed behind the repository

`host.yaml` uses the local socket, so run `sync` on the host (or as root through `ssh`); root is a global admin on the
socket. For a remote controller use `url`, `fingerprint` and `token_secret` as in [multi-host-fleet](../multi-host-fleet).

