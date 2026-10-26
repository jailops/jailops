# multi-host-fleet

Two hosts (`eu1`, `eu2`), **one project across both**, secrets in git encrypted with **age** and **SOPS**, host groups and a
staged rollout. Read `docs/fleet-sync.md` first for the rules; this is the example that uses all of them.

    hosts/eu1, hosts/eu2        how to reach each host (token for eu1, token and pinned mTLS certificate for eu2)
    groups/prod.yaml            @prod: every host labelled env=prod
    shared/networks, images     the same network and image on every host
    profiles/small.yaml         a repository-only fragment merged into jails
    projects/site.yaml          hosts: ["@prod"]: defined once, the same UUID on both hosts
    projects/site/profiles      the project's profile: network, storage, firewall, proxy
    projects/site/jails         web-eu1 and web-eu2: each declared on exactly one named host
    rollouts/web-upgrade.yaml   canary first, one jail at a time, a health gate between batches
    secrets/*.age, shared/secrets.sops.yaml, hosts/eu2/remote.sops.yaml, .sops.yaml

## Secrets, two kinds, which can be mixed

- `secrets/<name>.age` is one age-encrypted file per secret, used for the **API token of each host** (`token_secret: eu1-token`).
- `shared/secrets.sops.yaml` is a SOPS file (values encrypted, keys readable) for **values a jail receives**: `API_KEY:
  secret:site.api_key`. `hosts/eu2/remote.sops.yaml` holds the client certificate of eu2's pinned mTLS identity
  (`url` and `server_fingerprint` stay readable).

Sync decrypts a value in memory only when it applies it, seals it to the host's own age key (so only that host can open it,
and only when it starts a process in the jail) and never prints it. `plan` needs no decryption key: a secret's change is
detected from a digest of the stored ciphertext.

The example files are encrypted to the **throwaway identity** `examples/example-identity.txt`, which protects nothing real:

    export SOPS_AGE_KEY_FILE=examples/example-identity.txt JAILOPS_SYNC_AGE_IDENTITY=examples/example-identity.txt
    jailops-sync -C examples/multi-host-fleet secrets check     # opens the tokens; prints nothing secret
    jailops-sync -C examples/multi-host-fleet vault list-recipients

For your own fleet:

    jailops-sync secrets keygen > operator.key                  # private key on stdout, public on stderr (or age-keygen; or a plugin identity)
    jailops-sync vault add-recipient operator age1...           # edit .sops.yaml and re-wrap the SOPS files
    jailops-sync secrets set eu1-token                          # an API token of a global admin, encrypted into secrets/
    jailops-sync secrets put site.api_key                       # a value of shared/secrets.sops.yaml
    jailops-sync remote bootstrap eu2 --url https://eu2.example.org:8443 --fingerprint <sha256>   # mTLS, prints the trust command

Removing a recipient is revocation only after the re-wrap, and a removed key may already have read the current values:
rotate them.

## Day to day

    jailops-sync validate && jailops-sync plan --host @prod     # in a pull request: what would change
    jailops-sync sync --apply                                   # on merge
    jailops-sync drift                                          # in cron or CI: non-zero when a host differs from git
    jailops-sync watch --once --apply                           # a pull-based pass (git fetch, ff-only, plan, apply)
    jailops-sync rollout run web-upgrade                        # canary, then the rest, health-gated
    jailops-sync adopt --claim                                  # take over what already runs, as YAML

Sync changes only what it owns (`managed_by: sync:multi-host`); anything else is reported, never touched. `prune-check`
lists live objects the repository does not declare and deletes nothing.

## Verified, and not

`validate`, `plan`, `sync --apply`, drift reverted, and a SOPS secret sealed to a host's own age key and opened inside the jail are
verified on FreeBSD. **UNVERIFIED:** age plugin identities, `remote bootstrap` against a real host, and rollouts on real jails
(tested with fakes), this exact repository on two real hosts.
