# The first plan

What `jailops-sync plan` prints for this fleet against a host that has nothing yet. The test suite compares it with
the real output on every run, so this page cannot go stale (regenerate with `JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e`).

```
host self
  create    Image/freebsd-15
  create    Network/apps
  create    Project/apps
  create    FirewallPolicy/apps/floor
  create    FirewallPolicy/apps/hello
  create    Jail/apps/hello

projects
  apps
    self         will be created

6 create, 0 update, 0 delete, 0 unchanged, 0 unmanaged
```
