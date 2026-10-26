# The first plan

What `jailops-sync plan` prints for this fleet against a host that has nothing yet. The test suite compares it with
the real output on every run, so this page cannot go stale (regenerate with `JAILOPS_SYNC_REGEN_EXAMPLE=1 go test ./internal/fleet/e2e`).

```
host hz1
  create    Image/freebsd-15
  create    Network/blog
  create    Network/mail
  create    Project/blog
  create    Project/mail
  create    FirewallPolicy/blog/floor
  create    FirewallPolicy/blog/web
  create    FirewallPolicy/mail/floor
  create    FirewallPolicy/mail/mx
  create    Jail/blog/site
  create    Jail/mail/mx

projects
  blog
    hz1          will be created
  mail
    hz1          will be created

11 create, 0 update, 0 delete, 0 unchanged, 0 unmanaged
```
