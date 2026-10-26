# minimal-fleet

The smallest complete fleet repository: two hosts, projects `web` and `db`, a shared and a project profile, a firewall policy
per project, SOPS and age secrets, mTLS to one host and a rollout. `docs/fleet-sync.md` explains every file. The identity in
`../example-identity.txt` opens its secrets and protects nothing real.

    jailops-sync -C examples/minimal-fleet validate
    jailops-sync -C examples/minimal-fleet render --host web1      # the documents sync would send; nothing is decrypted

Larger, task-shaped examples are listed in [../README.md](../README.md).
