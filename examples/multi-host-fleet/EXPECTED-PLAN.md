# The first plan

What `jailops-sync plan` prints for this fleet against a host that has nothing yet. The test suite compares it with
the real output on every run, so this page cannot go stale (regenerate with `JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e`).

```
host eu1
  create    Image/freebsd-15
  create    Network/web
  create    Project/site
  create    FirewallPolicy/site/floor
  create    FirewallPolicy/site/web
  create    Jail/site/web-eu1
host eu2
  create    Image/freebsd-15
  create    Network/web
  create    Project/site
  create    FirewallPolicy/site/floor
  create    FirewallPolicy/site/web
  create    Jail/site/web-eu2

projects
  site
    eu1          will be created
    eu2          will be created

12 create, 0 update, 0 delete, 0 unchanged, 0 unmanaged
```
