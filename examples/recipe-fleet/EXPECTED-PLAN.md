# The first plan

What `jailops-sync plan` prints for this fleet against a host that has nothing yet. The test suite compares it with
the real output on every run, so this page cannot go stale (regenerate with `JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e`).

```
host h1
  create    Image/freebsd-15
  create    Network/shop
  create    Project/shop
  create    FirewallPolicy/shop/db
  create    FirewallPolicy/shop/web
  create    Jail/shop/db
  create    Jail/shop/web
  recipe apply     Jail/shop/db postgres-listen (new jail)
  recipe apply     Jail/shop/web nginx-site (new jail)

projects
  shop
    h1           will be created

7 create, 0 update, 0 delete, 0 unchanged, 0 unmanaged, 2 recipe(s) to apply
```
