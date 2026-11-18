# ipv6-routed-hetzner

A server for which the provider **routes a whole `/64` to the host** (a dedicated server or a cloud server with a routed
prefix). The upstream already knows the prefix lives behind the host, so **no NDP proxy is involved**; the host just
forwards. Each project gets its own `/80` of the `/64` (a routed `/64` holds 65,536 of them):

| Project | Network | IPv6 `/80` | Jails |
|---|---|---|---|
| `blog` | `blog` | `2001:db8:abcd:1:1::/80` | `site` (public) |
| `mail` | `mail` | `2001:db8:abcd:1:2::/80` | `mx` (SMTP) |

Replace `2001:db8:abcd:1::/64` with the prefix your provider routes to you and `vtnet0` with the uplink.

## What the example shows

- Routed networks (no `ipv6.ndp_proxy`: the provider routes the prefix to the host) with a real gateway address on the gateway interface (`2001:db8:abcd:1:1::1`).
- Two projects, two networks, no sharing; `firewall_policies: [floor]` in both.
- Fixed IPv6 addresses for the public jails (`::80`, `::25`) so DNS and rules can name them.
- Dual stack: IPv4 is NATed through the host's address; `expose:` lets the web ports in over IPv6 and forwards them over IPv4.

## Before you apply

- Forwarding must be on (`gateway_enable`, `ipv6_gateway_enable`; install.md section 4) and `pf.conf` needs the three
  anchor lines.
- Nothing in `pf.conf` is needed for the jails: an `allow in` rule of a policy opens the uplink for the jail (install.md section 3).

## Run it

    jailops-sync -C examples/ipv6-routed-hetzner validate
    jailops-sync -C examples/ipv6-routed-hetzner plan       # compare with EXPECTED-PLAN.md
    jailops-sync -C examples/ipv6-routed-hetzner sync --apply

## Limits

JailOps does not carve `/80`s out of the `/64` for you: you choose each `/80`, as this example does.

