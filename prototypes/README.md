# JailOps prototypes

Nine experiments that settled the assumptions of `docs/design.md` section 16.

**Status:** prototypes 1 to 4 and 6 to 9 ran on FreeBSD 15.1 (results in design section 18); prototype 5, the NDP
proxy, was superseded by the proxy that works through the root helper (design 18.10). They were written on Linux,
compile for `GOOS=freebsd`, and their pure logic is unit tested on Linux. A behaviour a prototype assumes and the run
did not settle stays marked `UNVERIFIED:` in the source. To repeat one, a human runs the binaries on a FreeBSD 15.1
host and sends the output back. Expect some prototypes to need a fix on first contact: a failing run with its output
is still a useful result. Prototype 7 (Raft) is the exception that also runs on Linux.

**Prototype 8's scope note:** it verifies shared-bridge project isolation (design 8.1) with
ordinary tools only (ping, nc, ifconfig, pfctl, sysctl, and tcpdump as a passive observer). It
deliberately does not forge, inject or craft any packet: no raw Ethernet frames, no crafted
ARP/NDP/router-advertisement/DHCP messages, no poisoning tooling. If stronger assurance against an
active on-link attacker is wanted, an operator can run that kind of test by hand, with their own
tooling, in an isolated lab — it is intentionally not automated here.

This directory is its own Go module (`jailops.net/jailops/prototypes`) with its own
`go.work`, so the parent module is untouched.

## Prerequisites

- FreeBSD 15.1, root, `zfs`, `jail`, `jexec`, `jls`, `pfctl`, `ifconfig`, `tcpdump`, `pkg`, `etcupdate`.
- Prototype 8 additionally needs `nc`, `ping`, `ping6`, `sysctl`, and a read-only tree to
  nullfs-mount as each jail's rootfs (a clone of the host's `/`, or any minimal base tree).
- Prototype 9 additionally needs `setextattr`/`getextattr`/`lsextattr`, `getfacl`/`setfacl`
  (acltype=nfsv4), and `chflags`.
- A **scratch ZFS pool** that you can lose. Never use your system pool.
- Go 1.27.3 on the build machine. Build elsewhere and copy the binaries:

```sh
cd prototypes
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -o bin/ ./cmd/...
scp bin/* root@freebsd-host:/root/jop/
```

### File-backed scratch pool

```sh
truncate -s 16G /var/tmp/jop-pool.img
zpool create -O mountpoint=/jop-pool -O compression=lz4 jopscratch /var/tmp/jop-pool.img
export JAILOPS_PROTO_POOL=jopscratch
# remove it when finished:
zpool destroy jopscratch && rm /var/tmp/jop-pool.img
```

Prototype 4 needs no more than a few hundred MB; prototype 2 and 6 need about 4 GB each (use
the 16 GB image). Prototype 9 needs about 1 GB (two 8 MiB files, a 16 MiB sparse file, and the
round-trip copy). Prototype 5 does not need a pool; prototype 7 needs nothing from FreeBSD;
prototype 8 needs no pool either (it uses nullfs, not ZFS jail roots).

## Safety rules, enforced by `internal/harness`

- Refuses to run without `--yes-i-have-a-scratch-pool` (not needed for `--dry-run`), without root,
  and off FreeBSD (except `--dry-run`).
- Every ZFS command is checked: datasets must lie under `$JAILOPS_PROTO_POOL/jailops-proto-<n>`;
  anything else aborts the run before it executes.
- Interfaces, bridges and jails created by a prototype are named `jop...`. Anchors are
  `jailops/jop3...` (prototype 3) and `jailops/jop8/...` (prototype 8). `pf.conf` is read, never
  written (it needs the same three `jailops/*` lines for both prototypes 3 and 8).
- Prototypes 8 and 9 also carry their own watchdog (prototype 8: a detached helper that restores
  the bridge/jails/PF anchors/sysctls; prototype 9 has nothing that can affect host networking, so
  it relies on `--cleanup` alone).
- `--dry-run` prints every command that would run and executes nothing.
- `--cleanup` removes what the prototype created and nothing else.
- The last line of output is a JSON verdict:
  `{"prototype":N,"pass":bool,"checks":[{"name","ok","detail"}],"unverified_resolved":[...]}`.
  Experiments whose answer is information (a hypothesis confirmed or refuted) record `ok:true` with
  the finding in `detail`; `ok:false` means a harness error or a failed acceptance test.

Common flags: `--pool`, `--yes-i-have-a-scratch-pool`, `--dry-run`, `--cleanup`, `-v`, `--cmd-timeout`.

## Run order

Always dry run first and read the plan.

| Order | Binary | Needs | Risk |
|---|---|---|---|
| 1 | `p1-jailapi` | pool | creates one persistent jail `jop1test` |
| 2 | `p4-zfsrepl` | pool | datasets only |
| 3 | `p9-capture` | pool, `setextattr`/`getfacl`/`chflags` | datasets only |
| 4 | `p3-pfanchor` | pf module, pf.conf with the three `jailops/*` anchor lines | loads rules into `jailops/jop3*` anchors only |
| 5 | `p2-thinupgrade` | pool, network or a pkgbase repo | jails, datasets |
| 6 | `p6-alpine` | pool, network, `linux64` | jail, bridge `jopbr6`, epairs |
| 7 | `p8-isolation` | pf module, pf.conf with the three `jailops/*` anchor lines, a read-only base tree | bridge `jop8br0`, 3 jails, epairs, `jailops/jop8/*` anchors, touches 5 sysctls (restored) |
| 8 | `p7-raft` | nothing | localhost ports |
| 9 | `p5-ndp` | a switched IPv6 /64 uplink, console access | **can disturb host networking**; run last, from a console or out-of-band, watchdog armed |

```sh
./p1-jailapi --dry-run
./p1-jailapi --yes-i-have-a-scratch-pool 2>&1 | tee p1.out
```

Per-prototype flags: run the binary with `-h`.

## Cleanup

```sh
./pN-<name> --yes-i-have-a-scratch-pool --cleanup
```

Run it for every prototype you started, then destroy the file-backed pool (above). For
prototype 5, if the watchdog did not fire, also check `ifconfig -l | tr ' ' '\n' | grep ^jop`
and `netstat -rn -f inet6 | grep jop`. For prototype 8, if its watchdog did not fire, also check
`ifconfig -l | tr ' ' '\n' | grep ^jop8`, `jls`, `pfctl -a jailops/jop8 -s Anchors -r`, and the five
sysctls named in its plan (`sysctl net.link.bridge.pfil_member net.link.bridge.pfil_bridge
net.link.bridge.pfil_onlyip net.inet.ip.forwarding net.inet6.ip6.forwarding`).

## What to send back

For each prototype: the complete terminal output (`tee pN.out`), including the final JSON line;
for 5 also the capture files from its recipe; for 8 also the results matrix (the "results matrix"
check's detail, a JSON blob of every flow tested) and a one-line conclusion per test (1-5) using
the template in its notes below; for any failure, `freebsd-version -kru`, `uname -a`,
`zpool get version,feature@encryption <pool>` and `kldstat`. Do not edit the output.
Also tell us anything that surprised you, and whether a prototype hung or left debris.

## Per-prototype notes

- **p1-jailapi**: `jail_set/jail_get/jail_remove` through `SYS_JAIL_*`, compared with `jls`; `LOCAL_PEERCRED` as root and as `nobody`.
- **p2-thinupgrade**: pkgbase generations, clone-based thin jail, etcupdate upgrade, swap, rollback `root` vs `all`
  (`zfs get written`), merge-conflict and `/var` drift cases. Prints the persistent path list that worked.
  Flags: `--pkgbase-dir` (offline repo), `--repo-conf`, `--abi`, `--var-paths`, `--mnt`.
- **p3-pfanchor**: needs the three `jailops/*` lines in `/etc/pf.conf` (`--pf-conf` to point elsewhere). Never edits it.
- **p4-zfsrepl**: clones, bookmarks, holds, resume tokens, raw encrypted send, guid chains. State in `/var/tmp/jailops-proto-4`.
- **p5-ndp**: `p5-ndp --yes-i-have-a-scratch-pool --pool x --jail-addr <unused addr in the /64> --peer-ssh root@peer --peer-dev <if> --parts A,B,C`.
  `--print-recipe` prints the tcpdump recipe; `--watchdog <seconds>` restores state if you lose connectivity.
  Part D (uplink inside the bridge) needs `--part-d-uplink-in-bridge --i-accept-losing-connectivity`; run it only from a console.
  No ZFS is used, but the harness still wants `--pool` or `$JAILOPS_PROTO_POOL`.
- **p6-alpine**: `--alpine-tarball`/`--alpine-url --alpine-sha256` for offline use, `--devfs-ruleset`, `--mount-base`.
  It prints a table of everything that failed.
- **p7-raft**: `--libs hashicorp,etcd --mode inproc|processes --ops N`. Needs no FreeBSD feature. On Linux run
  `go test ./internal/raftproto/scenario` (the binary itself refuses non-FreeBSD except `--dry-run`).
  cowsql (a CGO, SQLite-based Raft library) is deliberately not built here.
- **p8-isolation**: `--base-dir <path>` is required (a read-only tree nullfs-mounted as each jail's
  rootfs; a clone of the host's `/` is the simplest choice). Flags: `--mount-base`, `--watchdog`,
  `--rate-limit`, `--test-port`, `--skip-throughput`. Needs the same three `jailops/*` lines in
  `/etc/pf.conf` as prototype 3. It verifies configuration and connectivity only (ping, nc,
  ifconfig, pfctl, sysctl, tcpdump as a passive observer) — see the scope note above the
  prerequisites. Tests, each recorded in the results matrix (a JSON blob printed as one `Check`'s
  detail, and in `unverified_resolved`):
  1. direct L2 reachability between jails on the shared bridge, `private` member flag on and off;
  2. routed-through-the-host-gateway reachability with per-project PF anchors and the
     `net.link.bridge.pfil_member`/`pfil_bridge`/`pfil_onlyip` sysctls (which combination is needed
     is itself unverified and is what this test settles);
  3. address binding: an aliased or reconfigured foreign address probed with ordinary `ping -S`,
     with and without a per-jail `block in on <epair> from !<addr>` anchor;
  4. `ff02::1` and the IPv4 subnet broadcast, observed (not injected) with tcpdump, private on/off;
  5. simultaneous TCP throughput between two jail pairs, with and without a PF
     `max-src-conn-rate` limit (a connection-rate limit, not a bandwidth shaper) — numbers only, no
     guarantee claimed.

  **Conclusion template (paste back filled in):**
  ```
  p8-isolation on FreeBSD 15.<rev>, <date>

  1. private=on suppresses direct L2 reachability:        yes / no / partially: <detail>
  2. pfil_* combination that made PF filter bridge traffic: <combo, or "none found">
  3. per-jail anchor stops an aliased/reconfigured foreign source: yes / no / partially: <detail>
  4. ff02::1 / subnet broadcast crosses the private boundary:      yes / no: <detail>
  5. throughput with/without max-src-conn-rate (measured, not a claim): <numbers>

  Overall: isolation design 8.1's three mechanisms (private members, PF anchors, antispoof-style
  address binding) verified as described / verified with caveats / refuted, because: <why>
  Active-attacker tests (forged ARP/NDP/RA/DHCP) NOT run by this tool; <run separately? yes/no>
  ```
- **p9-capture**: flags `--mount-base`, `--seed`, `--fuzz-ops`, `--many-snapshots`,
  `--skip-roundtrip`, `--skip-many-snapshots`. Builds a mixed tree (small/large/sparse files, hard
  links, symlinks, a long name, extattrs, NFSv4 ACLs, file flags) under a dataset with
  `acltype=nfsv4`, with a writer goroutine running during snapshots for crash-consistency. Tests:
  1. snapshot metadata fidelity (live stat vs `.zfs/snapshot` view) and inode/ctime stability
     across two snapshots, a `zfs rollback`, a clone, and a `send|recv` (the last two are
     informational: a clone or a receiver is a new dataset and is not expected to share inode
     numbers — row 9);
  2. a seeded, reproducible random operation sequence compared via a full L0 walk against
     `zfs diff -FHt`, batch by batch, reporting every disagreement with the exact operations that
     caused it (row 10; rerun with the same `--seed` to reproduce a disagreement);
  3. a minimal, independent archiver/extractor (stdlib + `golang.org/x/sys/unix` only, no
     repository code) round-trips a snapshot tree into a fresh dataset; a metadata digest, a
     content digest and hard-link-group membership are compared (rows 11-12);
  4. `.zfs/snapshot` automount delay, `snapdir` visibility, listing behaviour with many snapshots,
     and reading a file via an fd opened under `.zfs/snapshot` while that snapshot is destroyed.

  Says explicitly, in its `unverified_resolved` entries, which of mtime/ctime/inode/xattr/ACL/flags
  can be trusted from a snapshot read alone and which forced a full read in this run.

Build and check: `go vet ./... && go test ./... && GOOS=freebsd go build ./...`
