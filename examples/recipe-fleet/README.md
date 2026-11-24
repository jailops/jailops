# recipe-fleet

One host, project `shop`, two jails and two firewall policies, with the configuration inside the jails kept by recipes:

- `db` runs `recipes/postgres-listen` (a template file, the service, a reload).
- `web` runs `recipes/nginx-site` and **needs** `db`: its recipe runs after the one of `db` and waits when `db` is not settled.
- The policy `db` lets only `jail:web` reach PostgreSQL. Sync makes policies before jails; a rule that names a jail that does not
  exist yet matches nobody until it does.

    jailops-sync -C examples/recipe-fleet validate      # offline: renders every recipe and checks it
    jailops-sync -C examples/recipe-fleet plan          # per recipe: what the dry run found and what sync would do
    jailops-sync -C examples/recipe-fleet sync --apply
    jailops-sync -C examples/recipe-fleet drift         # 1 when a recipe would be applied

`docs/fleet-sync.md` explains the decision (hash, dry run, `needs`, exit codes). A recipe lives in a directory of the repository; a
directory of the Recipe Book that is a git submodule is used the same way (`path: recipes/book/nginx-tls-site`).
