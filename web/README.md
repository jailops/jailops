# JailOps web consoles

One repository, several small consoles. Each console is an app in its own directory, shares only
`shared/`, talks to one kind of server, and is embedded in one Go binary. `apps.json` is the registry;
the build tooling, the architecture tests and the Go embed tests are all driven by it.

| App | Binary | Embed package | Dist directory | API role | URL prefixes it may call (under `/api/v1`) |
|---|---|---|---|---|---|
| `host` | `jailopsd` | `internal/webui` | `internal/webui/dist` | host (also hub) | jails, projects, storage, trust, sessions, profiles, updates, images, apps, networks, firewall, backup-targets, backup-policies, backups, restores, remotes, operations, events, audit, users, certificates, resources, plus the shared set |
| `backup` | `jailops-backupd` | `internal/backupui` | `internal/backupui/dist` | backup | repo, hosts, enrolment, snapshots, retention, gc, verify, quarantine, maintenance, deletions, operations, events, audit, plus the shared set |

The shared set (`shared/src/api/core.ts`) is the server info at `/api/v1`, `auth`, `doctor` and `tokens`.
The host console shows only the host side of backup (targets, policies, snapshots, last runs, restore);
the backup console has no jail, image, network or firewall code.

## Layout

```
web/apps.json       registry (single source of truth)
web/shared/         tokens and base CSS, theme, session, events, operations, API client base, generic
                    components (shell, modal, login, token manager), types.gen.ts. No feature views.
web/host/           the host and hub console
web/backup/         the backup-node console
web/templates/app/  scaffold template for a new console; web/templates/go/ for its embed package
web/tests/          architecture tests (boundaries, registry, index.html identity)
web/scripts/        app.mjs (build, dev, typecheck, e2e) and new-app.mjs
```

`types.gen.ts` is generated from `sdk/api` by `go run ./scripts/gen-ts` (check with `-check`).

## Commands (from `web/`)

| Command | Does |
|---|---|
| `npm ci` | install all workspaces |
| `npm run build -- --app host` / `npm run build:all` | build into the app's registered dist directory |
| `npm run dev -- --app backup` | dev server on the app's port; `/api/v1` is proxied to the env var named in the registry (`JAILOPS_API`, `JAILOPS_BACKUP_API`) |
| `npm run typecheck` | vue-tsc for every app (shared is compiled with each) |
| `npm test` / `npm run test:all` | vitest for every app, shared and the architecture tests |
| `npm run e2e -- --app host` / `npm run e2e:all` | Playwright in demo mode against the built app (build first) |
| `npm run new-app -- <name> [--role host\|hub\|backup] [--title T] [--label L] [--binary B] [--prefixes a,b]` | scaffold a console |

Append `?demo` to any URL to run a console on canned data without a server (`?demo=login` starts signed out).

## Apps and logs (host console)

`/apps` is the app catalog and its Installed tab, `/apps/:name` one definition with the install dialog
(`src/views/AppsView.vue`, `AppDetailView.vue`, `src/components/AppInstallDialog.vue`). The install follows the
operation like every other dialog (`runOp`), with a poll that also reads the operation's progress: the server sets it
after each step, so `lib/apps.ts` maps it onto the step list of the dry run. The jail's Logs tab
(`src/components/JailLogsTab.vue`) reads `/jails/{name}/logs`; following polls once a second from `next_offset`.
`lib/logs.ts` holds the follow state and strips terminal control characters; log lines are only ever rendered as
text. The demo serves a catalog, installations and growing log files (`src/demo/apps.ts`); `?demo` jail names that
start with `fail` show a failed installation.

## Rules, enforced by `web/tests/architecture.test.ts`

- An app imports `shared` and itself, never another app. Shared imports no app.
- An app's API module (`src/api/`) is the only place that talks to the network, and it may only call the
  first path segments listed under `prefixes` of its registry entry.
- Shared code has no feature views and no app-specific endpoints.
- Every `index.html` carries its own title, `jailops-app` and `jailops-role` meta and a visible header, so
  consoles cannot be confused. Titles, ports, dist directories and binaries are unique.
- An app's `vite.config.ts` is one `appConfig('<name>', import.meta.dirname)` call; the output directory
  comes from the registry only.
- Go side (`internal/<pkg>/embed_test.go`): the embedded index belongs to its app and to no other, and
  `dist/.gitkeep` exists so `go:embed` compiles before the first build.

## Add an app

1. `npm run new-app -- inventory --role host --prefixes inventory,items` creates `web/inventory`
   (router, layout on `shared`, demo mode, tests, e2e smoke), registers it in `apps.json` and `package.json`,
   and creates `internal/inventoryui` (embed handler and tests) if it does not exist.
2. `npm install`, then `npm run build -- --app inventory`, `npm run test:all`.
3. Serve `inventoryui.Handler()` from the binary and fix `binary`, `embedPackage` and `prefixes` in the registry.
4. Put every endpoint in `src/api/endpoints.ts`, canned data in `src/demo/`, copy in `src/i18n/en.ts`.
