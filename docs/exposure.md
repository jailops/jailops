# Exposing the API (and the web console) safely

What is written here is what the code does; what is not implemented is in **Not yet** at the end. Nothing on this page
is needed to use JailOps on one host: the default is the local socket and nothing else.

## What listens

| Listener | Default | What it is |
|---|---|---|
| unix socket `listen.unix.path` | `/var/run/jailops/jailops.sock`, mode `0666` | the local API, one identity system with the TLS listener. The only thing the **peer's uid** decides is root (uid 0): a global admin, the host's recovery access. Every other local user is anonymous, exactly like an unauthenticated remote client (it reaches `GET /api/v1`, `GET /healthz`, the login, trust-token redemption, `POST /api/v1/cluster/join` (501) and the static console, nothing else) and signs in with a token or a password login. No Unix user or group maps to a role. Mode `0666` is safe for that reason |
| TCP `listen.https.address` | **empty: no network listener** | the same API over **TLS 1.3 only**, plus the web console |

There is no plain-http TCP listener in `jailopsd`. A listener that is not TLS cannot be configured, so the daemon never
listens without TLS on a non-loopback (or any) address; a test connects with plain http and with TLS 1.2 and both fail. `listen.https.address` is
checked when the configuration is loaded (`":39110"` is every interface, `"127.0.0.1:39110"` and `"[::1]:39110"` are loopback only; a
wrong form is an error). The SDK, in turn, refuses plain `http://` to anything but loopback.

JailOps does not open ports for you: the host's `pf.conf` decides who can reach `listen.https.address`. Admit the addresses of your
administrators and nobody else.

## The web console: built in, off until you turn it on

The console is the Vue application embedded in `jailopsd` (and the backup console in `jailops-backupd`). It is in every binary and
package, and it is **off by default**: the daemon and `jailops-sync` do not use it. Turn it on in one of three ways:

- **In the configuration**, `jailopsd.yaml`:

      web:
        enabled: true

  Without it (the default, or `enabled: false`) every path outside `/api/v1` and `/healthz` answers 404 and the API is unchanged.
- **From rc.conf**: `sysrc jailopsd_web="YES"` (then `service jailopsd restart`) starts the daemon with `-web`, whatever
  `web.enabled` says. `jailopsd_web="NO"` forces it off over the configuration. Unset, the configuration decides.
- **With a flag**: `jailopsd -web` and `jailopsd -no-web`.

For a daemon that has no console at all, `go build -tags noweb ./cmd/jailopsd` (`just build-noweb`) leaves the embedded application
out of the binary; `/` then answers 404 "this build has no web console". `just build` never does: it always builds everything in.

When the console is on it is served by the same listeners (the socket and the TLS listener, default port 39110), under `/`, and its
login routes (`/api/v1/auth/...`) exist whether or not the console is on: a user with a password can still log in through the API.
Create no users (`jailops user list`) if you want none. The backup node has the same switch: `web.enabled` (default `false`) in
`backupd.yaml` serves its console on the same port as the host API and the management API (39111); `web.listen` is gone. All
ports are listed in [ports.md](ports.md).

## The server certificate

| Option | Configuration | What happens |
|---|---|---|
| **Self-signed, pinned** (default) | nothing | on first start `jailopsd` generates an ECDSA P-256 certificate valid for **ten years**, with the host name and the entries of `listen.https.names` as names, into `<data_dir>/tls/server.{crt,key}` (key mode 0600). Clients **pin its SHA-256 fingerprint** (there is no trust on first use: a trust token carries it). It is not rotated by expiry |
| **Your certificate** | `listen.https.cert` and `listen.https.key`, absolute paths, both or neither | served as given (a file with the chain is fine). If a file is missing or only one of the two exists the daemon **refuses to start**: it never generates or overwrites a certificate you configured |
| **ACME** | not implemented | see Not yet; use `certbot`, `acme.sh` or `lego` and point the two keys at the files they keep current |

The fingerprint is in the start log line (`jailopsd started ... fingerprint=`) and can be read from the file:

    openssl x509 -in /var/db/jailops/tls/server.crt -noout -fingerprint -sha256

**Reload.** The certificate files are looked at on a TLS handshake, at most every 15 seconds. When their modification time or size
changed, the pair is parsed and used for new connections, without a restart. A pair that does not parse (a half-written renewal) is logged
(`tls certificate changed on disk but does not load; still serving the previous one`) and the previous certificate stays. The key
file must be readable by the daemon's user (`jailops`): for example `chown root:jailops` and mode `0640`.

**Pinning and renewal do not mix.** A client pins the leaf certificate's fingerprint, so a certificate that renews every 60 to 90
days breaks every pin. With a certificate a public CA signed, **do not pin**: leave `fingerprint` out of `jailops remote add`
(the client trusts a certificate the system roots verify, and pins only when none does) and out of `host.yaml` of a fleet. With the
self-signed default, replacing the certificate by hand (remove the two files, restart) changes the fingerprint and every client has to
pin the new one. Trust tokens carry the fingerprint the daemon read **at start**: after replacing a certificate, restart `jailopsd`
before you issue new trust tokens.

## How a caller proves who it is

All of these work on the TLS listener at the same time; a credential that is present but bad is refused (and counts toward the lockout).

1. **A pinned client certificate** (mTLS). The listener *requests* a client certificate; it never requires one. A presented
   certificate must be within its validity and be a client certificate, and is then looked up **by fingerprint** in the trust store
   (`jailops cert add`, or the join flow below). It has a role and a scope (a project list or `global`; an empty list is never
   "everything") and an optional expiry. An unknown certificate with no other credential is answered `unauthorized`.
2. **An API token** in `Authorization: Bearer`: one role, a scope, `--expires` and `--allow-from` (source networks).
   `jailops token create ci --permission operator --projects web --expires 720h --allow-from 203.0.113.0/24`. The secret is shown once.
3. **A user and password** (optionally TOTP) for the web console, which gets a session cookie (`Secure` when the connection is TLS,
   `SameSite=Strict`) and a CSRF token; mutations also check the `Origin` against the `Host` header.

The same three work over the unix socket (a client certificate needs TLS, so there it is a token or a session). Root on the host
needs none of them: uid 0 on the socket is a global admin. See [security-model.md](security-model.md).

Failed authentications from one source (a bad token, an untrusted certificate, a forged claim) are rate limited per source
(`limits.auth_failure_threshold`, default 5) and audited. Creating networks, images, storage and projects needs a **global admin**
identity, so the identity of `jailops-sync` is one.

Join flow for a person or a machine, pinned in both directions:

    jailops trust add --name laptop --role admin --global --ttl 15m --cert-expires 2160h   # on the host: prints a single-use token
    jailops remote add myhost https://host.example.org:39110 --token-stdin                  # on the client: paste the token

## Recipe 1: local only (the default)

Do nothing: leave `listen.https.address` empty. To administer from your laptop without opening a port, forward the socket over ssh:

    ssh -N -L /tmp/jailops.sock:/var/run/jailops/jailops.sock admin@host
    JAILOPS_TOKEN=jot_... JAILOPS_SOCKET=/tmp/jailops.sock jailops list --project web

The daemon sees the peer credentials of the process that connects to its socket, which for a forwarded connection is `sshd` running as
the user who logged in (reasoned from the code; UNVERIFIED on a real host). For an ordinary user that is an anonymous peer, so the
forwarded socket needs an API token like any remote client; logging in over ssh as root would make the forwarded socket root's,
a global admin, so do not forward as root for routine work. For a fleet, run `jailops-sync` on the host as root, or over the
network with a token or certificate. Leave `web.enabled` off (the default) if nobody uses the console.

## Recipe 2: behind a reverse proxy or Cloudflare

The listener speaks TLS only, so a proxy must connect to it with **https**. The daemon's certificate is self-signed by default, so tell the
proxy to trust exactly that certificate, and let the proxy hold the public certificate.

    # jailopsd.yaml
    listen:
      https:
        address: "127.0.0.1:39110"          # loopback: only the proxy on this host can reach it
        names: [jailops.internal]          # the name the proxy verifies
    web:
      enabled: false                       # API only, unless you want the console behind the proxy too

    # nginx, as one example of a proxy
    location / {
        proxy_pass https://127.0.0.1:39110;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /var/db/jailops/tls/server.crt;   # the daemon's own certificate, as the only trust anchor
        proxy_ssl_name jailops.internal;
        proxy_set_header Host $host;                # the console's CSRF check compares Origin with Host: keep the original
        proxy_http_version 1.1;
        proxy_buffering off;                        # the events stream is long-lived
        proxy_read_timeout 1h;
    }

What changes behind a proxy, all of it following from the code:

- **No client certificates.** TLS ends at the proxy, so pinned mTLS is not available through it: callers use API tokens (or the
  console login). Keep tokens short-lived and scoped, and put an access control **in the proxy** (Cloudflare Access, an IP allow
  list, basic auth): that is the outer door, and the token is the inner one.
- **The daemon sees the proxy's address**, not the caller's. The lockout counts failures per source, so one attacker behind the proxy can
  lock the proxy's address out for everyone, and a token's `--allow-from` can only name the proxy. `X-Forwarded-For` is not
  used. Do not rely on `--allow-from` here.
- **`exec`, `shell` and the console** use an HTTP `Upgrade` to a JailOps protocol (`jailops-exec/1`), not a WebSocket. A proxy that passes
  only WebSocket upgrades (a CDN, for instance) will not carry them; use ssh for shells and the proxy for the API. UNVERIFIED with any
  specific proxy or with Cloudflare; the facts above are from the code.
- **A tunnel** (a connector that dials out from the host) has the same shape: point it at `https://127.0.0.1:39110` and give it the
  daemon's certificate as its CA and the name above, rather than turning verification off. UNVERIFIED with `cloudflared`.
- With a certificate for the public name on the proxy and the daemon's self-signed one behind it, **clients pin nothing**: they verify the
  proxy's public certificate, and `host.yaml` has no `fingerprint`.

## Recipe 3: direct, with TLS and mTLS

The strongest setup JailOps has today: the listener on a public or private address, a certificate you control, and every administrator and
`jailops-sync` identified by a **pinned client certificate**, no tokens and no passwords.

1. Configure the listener and, if you have one, your certificate:

       listen:
         https:
           address: "203.0.113.10:39110"       # or ":39110"; bind the address you mean
           names: [host.example.org, 203.0.113.10]     # names of the generated certificate; unused with cert and key below
           # cert: /etc/ssl/jailops/fullchain.pem
           # key:  /etc/ssl/jailops/privkey.pem
       web:
         enabled: false

   `service jailopsd restart`, then `jailops doctor`.
2. Let only your administrators' addresses reach it, in `pf.conf` (JailOps never opens a port):

       pass in quick on vtnet0 proto tcp from { 203.0.113.7, 198.51.100.0/24 } to (vtnet0) port 39110

3. Give each person a pinned identity, valid for a limited time: the trust-token join flow above, with `--cert-expires`. For a fleet,
   make the client certificate of each host in the repository (`hosts/<host>/remote.sops.yaml`, encrypted; the plaintext key never
   touches the disk) and trust it on the host:

       jailops-sync remote bootstrap web1 --url https://web1.example.org:39110 --fingerprint <sha256 of the server certificate>
       # it prints the command to run on web1 as an administrator; run it there

   The connection refuses a server whose certificate does not match the fingerprint. With your own CA-signed certificate leave the
   fingerprint to the system roots instead (see "Pinning and renewal").
4. **Take the tokens and passwords away.** The listener requests a client certificate but does not require one, so a token that
   exists works from anywhere the firewall lets in: `jailops token list` and `jailops token delete`, no `jailops user create`, and
   no `token_secret` in `host.yaml`. A client that presents no certificate and no token reaches only the public routes: `GET /api/v1` (the
   version, the extensions, and whether it is trusted), `GET /healthz`, `POST /api/v1/auth/login` (which needs a valid user and
   password), `POST /api/v1/trust/clients` (which needs a valid single-use trust token), `POST /api/v1/cluster/join` (answers 501) and the
   static console files. A non-root local user of the unix socket is exactly such a client.
5. Revoke by deleting the identity: `jailops trust revoke NAME|FINGERPRINT`. There is no CRL; the trust store is the list.

**UNVERIFIED on a real network:** the whole recipe end to end on a public address, a CA-signed certificate with renewal on a real host,
`jailops-sync` over the network with a CA-signed certificate. What the repository's tests do prove, with in-process daemons: TLS 1.3 only
and no plain http, a served and a reloaded certificate, a broken renewal ignored, a configured certificate never generated over, pinned
client certificates and the join flow, the web console switched off.

## Not yet

- **ACME** (automatic certificates). Use an external client and `listen.https.cert`/`key`; reload makes renewal work without a restart.
- **Requiring a client certificate** (an mTLS-only mode): the listener requests but never requires one, so tokens and logins that
  exist keep working. Client certificates are trusted one by one by fingerprint; there is no client CA, no CRL, no OCSP.
- **The caller's address behind a proxy** (`X-Forwarded-For`, the PROXY protocol): the lockout and `--allow-from` see the proxy.
- **A plain-http listener on loopback** for a proxy (the listener is TLS only, so the proxy needs `https://` upstream).
- **Separate listeners** for the API and the console, or a console switch per listener: `web.enabled` is for the daemon.
- **Trust tokens that follow a reloaded certificate**: they carry the fingerprint read at start; restart after replacing it.
- A hub that presents a CA-signed certificate as its client identity: the hub reuses the server certificate for its client
  connections to hosts, which is meant for the self-signed one. Run a hub with the default certificate.
