# JailOps web consoles

One repository, several small consoles. Each console is an app in its own directory, shares only
`shared/`, talks to one kind of server, and is embedded in one Go binary. `apps.json` is the registry;
the build tooling, the architecture tests and the Go embed tests are all driven by it.

| App | Binary | Embed package | Dist directory | API role | URL prefixes it may call (under `/api/v1`) |
|---|---|---|---|---|---|
| `host` | `jailopsd` | `internal/webui` | `internal/webui/dist` | host (also hub) | jails, projects, storage, trust, sessions, profiles, updates, images, apps, networks, firewall, backup-targets, backup-policies, backups, restores, remotes, operations, events, audit, users, certificates, resources, logs, plus the shared set |
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
| `npm run e2e -- --app host` / `npm run e2e:all` | Playwright against a demo build of the app (made into `<app>/.demo-dist` by the test's web server) |
| `npm run new-app -- <name> [--role host\|hub\|backup] [--title T] [--label L] [--binary B] [--prefixes a,b]` | scaffold a console |

In a demo build (`JAILOPS_DEMO_BUILD=1`, which the end-to-end tests make; `JAILOPS_DEMO_BUILD=1 npm run dev -- --app host`
for the dev server) append `?demo` to any URL to run
a console on canned data without a server (`?demo=login` starts signed out). The console a binary embeds is never a
demo build: it ignores `?demo`.

## Images and the base image (host console)

An image is one FreeBSD release (`freebsd-15-1`); its generations are the patch levels. `/images` shows each image as a
card (the nightly upgrade in `src/components/ImageAutoUpgrade.vue`) with its release, its number of generations and its nightly auto upgrade: **Upgrade automatically every night**, the
time (HH:MM in the host's time zone, 03:00 by default) and **Also upgrade the packages inside the jails** (on when the
upgrade is turned on), saved with `PATCH /images/{name}` and `{spec: {auto_upgrade}}`. The card shows the next run and the
latest runs from `status.auto_upgrade` (time, ok, unchanged or failed with the detail, jails moved, upgraded and failed); a
failed latest run is a danger notice on the card, not a toast. Only a global admin changes it; everyone else sees it
read-only. **Upgrade image and jails** (by hand, at once) stays as it was.

**Create image** (`src/components/ImageCreateDialog.vue`, the fields in `ImageFields.vue`, the checks in `lib/images.ts`)
posts the definition and then, unless the box is cleared, builds the first generation and follows that operation with its
progress (`composables/imageCreate.ts`; a failed build keeps the definition and Try again builds it again). The release
defaults to the host's (`environment.kernel_version` of `GET /api/v1` without the patch level) and nothing older than 15.1
is offered; the architecture is the host's. The name is proposed from the release (`freebsd-` major `-` minor, so
15.1-RELEASE gives `freebsd-15-1`) and follows the release select until it is edited. No alias is offered. The nightly auto
upgrade is a checkbox of the dialog, on by default for a new FreeBSD image. An empty page offers the dialog as its first
action, `/images?create=1` opens it, and the install dialog and the jail form offer it when there is no image; both list
the images with their release (`freebsd-15-1 (15.1-RELEASE)`) and preset the newest release the host can run. The first-run
wizard's Base image step uses the same fields and composable, with one line on what the auto upgrade does.

When the host runs a newer release than the newest FreeBSD image (the host on 15.2, only `freebsd-15-1`), the Images page
and the Overview show a quiet notice (`src/components/NewReleaseNotice.vue`) with a button that opens Create image filled in for the host's release: a new release
is a new image, and existing jails stay on theirs.

In the demo, an image whose name starts with `fail` fails to build, one that starts with `flaky` fails at its first try;
`?demo=noimage` is the demo without images and `?demo=newrelease` a host on 15.2 with only `freebsd-15-1`. The demo's
`freebsd-15-1` has the nightly upgrade on with an ok run and two unchanged ones; `?demo=nightlyfail` adds a failed night on
top. The demo accepts the PATCH and checks the time as the server does.

## Apps and logs (host console)

`/apps` is the app catalog and its Installed tab, `/apps/:name` one definition with the install dialog
(`src/views/AppsView.vue`, `AppDetailView.vue`, `src/components/AppInstallDialog.vue`). The install follows the
operation like every other dialog (`runOp`), with a poll that also reads the operation's progress: the server sets it
after each step, so `lib/apps.ts` maps it onto the step list of the dry run. The jail's Logs tab
(`src/components/JailLogsTab.vue`) reads `/jails/{name}/logs`; following polls once a second from `next_offset`.
`lib/logs.ts` holds the follow state and strips terminal control characters; log lines are only ever rendered as
text. The demo serves a catalog, installations and growing log files (`src/demo/apps.ts`); `?demo` jail names that
start with `fail` show a failed installation.

`/logs` (`src/views/LogsView.vue`) reads `GET /logs`: the ends of many jails' files merged by time, filtered from the
address (`?jail=web&since=1h`, `project=*` for every project) and followed every two seconds with the reply's `next` map as
the cursor (`lib/logs.ts`). Its Forwarding tab, for a global admin, edits the sink list of `/logs/forward`
(`lib/logforward.ts` checks the form); the demo serves both from `src/demo/logs.ts`.

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

## Interface conventions (shared by every console)

- **Confirmations** (`shared/src/composables/confirm.ts`, `ConfirmDialog.vue`): tier 1 runs at once (start, snapshot,
  preview); tier 2 asks and names what is affected (stop or restart a running jail, image upgrade, uninstall an app);
  tier 3 needs the name typed (delete a jail, data rollback, delete a project, a volume, a network with jails, a
  firewall policy in use). The button label alone is never the warning.
- **Names**: `shared/src/lib/names.ts` holds the server's name rules and their hints; a form never writes its own regex.
- **Forms** (`composables/form.ts`): required fields carry `*`; a field shows its message once touched or after a
  submit; a primary button is never silently disabled: the reason sits next to it and a submit focuses the first
  field with a problem.
- **Tabs** (`composables/tabs.ts`, `TabBar.vue`): the tab is in `?tab=`, the default tab is left out of the address,
  arrow keys, Home and End move between tabs.
- **Tables** on a phone: class `rtable`, a `data-label` on every cell, the row buttons in `td.actions` (a
  `.row-actions` group elsewhere).
- **Time and units**: `stamp()` / `TimeStamp.vue` (ISO date and time in UTC, the relative time in the tooltip),
  `bytes()` (binary units, a decimal only when it is not zero), `plural()` for counts, `sentence()` for words that come
  from the server. API paths and error codes are never shown as such: `lib/describe.ts` turns them into links and words.
- **Verbs**: Create and Delete for objects (jail, network, project, policy, token, user), Add and Remove for members
  of a list (a peer, a rule, a sink, a nameserver), Revoke for access (a token, a session, a certificate), Install and
  Uninstall for apps. A button says the verb and the object ("Create network"), a dialog title says it with the name
  ("Delete jail web"). Spelling is British, as in the documentation (neighbour, enrol, utilisation).
