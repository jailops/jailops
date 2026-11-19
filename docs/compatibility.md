# Compatibility between releases

This page answers one question for administrators and for people who write against the API: which releases work together,
and what happens when two of them meet. The rules are frozen by tests (`just freeze`, `internal/upgradecheck`, the
previous-client test) and change only on purpose.

## Clients, servers and hubs

- **An older client against a newer server works.** The server only adds: new members in objects, new routes, new
  extensions. A client ignores a member it does not know, in the server information, in a list and in an object
  (`TestClientReadsAnswersOfALaterServer`), and the previous release's client library runs against the current server
  (`TestPreviousSDKWorksAgainstTheServer`).
- **A newer client against an older server fails fast.** A request that needs an extension the server does not list returns
  `unsupported` (`errors.Is(err, sdk.ErrUnsupported)`) before it is sent. Capabilities are the extension names of
  `GET /api/v1` (`api_extensions`), never version numbers.
- **The server is strict about what it receives.** A request body with a member the type does not know, a member twice,
  a member in other letter case or data after the value is refused with `invalid` and the message names the member
  (`TestRequestBodiesAreStrict`); a merge patch is checked the same way. A misspelled option is an error, not a request
  that quietly does less. A client of a later release must therefore send a new member only to a server that lists the
  extension that introduced it.
- **A hub and a host of different releases work together.** The hub vouches for the caller with a signed claim in the
  `X-JailOps-Requestor` header. The claim has version 1. A host refuses a claim of another version and accepts a claim with
  a member it does not know, so a hub that adds a member keeps working against this host, and the members of the claim
  are frozen (`TestRequestorClaimWireIsFrozen`). The hub reads the host's extensions and sends nothing the host does not
  list.

## Lists and pagination

A list is always a JSON array of objects in the `metadata` of the response, never an object that wraps it, and a list without
a `limit` returns everything. If a collection ever needs paging, it is added the way extensions are: a client opts in
with the query parameters `limit` and `cursor`, and a server that pages says so in the `Link: <...>; rel="next"` header of
the response. A response without that header is complete. A client that does not know paging keeps getting whole lists, and
a client that asks for a page from a server that does not page gets the whole list and no `Link` header, which it reads as
"complete". Nothing in the body changes.

## Daemon and helper

`jailopsd` runs as an unprivileged user and asks the helper `jailops-priv` (root) for everything that needs root. The two
are one service and are upgraded together.

- **The hello.** Every connection starts with a hello that carries the protocol version (`ProtocolVersion`, now 1). A peer
  that speaks another version is refused on both sides with `version_skew`: the daemon says "helper speaks protocol N, this
  client speaks M" and the helper refuses the daemon. Nothing is attempted over a connection that failed the hello.
- **The allow-list is in the helper's binary.** The commands the helper may run are compiled in. A new daemon that needs a
  new command gets `forbidden` from an old helper until the helper restarts: a package upgrade is not complete until the
  helper runs the new binary.
- **The order.** `service jailopsd restart` stops the daemon, then the helper, and starts the helper first and the daemon
  after it (the helper `REQUIRE`s syslog and zfs and runs `BEFORE` the daemon at boot). `service jailops_priv restart`
  restarts the helper alone, and the daemon asks it again at its next call.
- **Running jails are not touched.** They are kernel jails. Restarting either service stops no jail; the daemon's loop reads
  the state again and loads what is missing.

## What a stop in the middle of a change leaves

- **A stored state is never half written.** An object is written to a temporary file, flushed and renamed; the previous copy is
  kept as `.previous`. A database whose schema is newer than the binary is refused with `version_skew`, and a document
  of a newer `apiVersion` is refused the same way: there is no silent downgrade. Before a database migration the daemon
  writes `jailops.db.before-schema-N`.
- **An interrupted upgrade of a jail is rolled back at the next start.** Every step of a jail upgrade (snapshot, new root,
  merge, swap) is journalled. When the daemon starts it marks the operations that were running as failed (`interrupted`)
  and runs the undo steps of every unfinished plan from the journal, so the pool is consistent and the jail can start on
  its old generation. The outcome of each run (`rolled_back`, `completed`, `dropped`, `needs_attention`) is in the log and in
  `jailops doctor`. A run that needs attention keeps its journal record: it is not dropped.
- **A rollback is not an undo of the data.** `jail rollback --scope root` restores the previous root; `--scope data` and `--scope all` also
  restore the persistent datasets and destroy what was written since. See [upgrade.md](upgrade.md#what-an-upgrade-of-a-jail-does).
- **The states of removed firewall rules.** When a firewall anchor is loaded, the labels of the rules that are gone are
  written to `pending-pf-kills.json` in the state directory before the new rules are loaded, and removed when pf has ended
  their connections. A daemon that stops in between ends them after the restart. A file that is missing is normal; a file
  this release cannot read is ignored with a log line and never blocks the start. Until the connections are ended,
  `FirewallApplied` of the jail is `False` with the reason `StatesPending`.
- **When pf cannot be asked.** If pfctl gives no answer or no status, the condition `FirewallApplied` is `Unknown` with the
  reason `Unverified`: the anchor holds the rendered rules, but nothing says that pf evaluates it. It is never `True` then.

## The rules for 1.x

1. **Versions.** A major release may break; a minor release only adds (routes, members, extensions, enum values, condition
   types and reasons, error codes, commands and flags); a patch release fixes. The Go client library has the version of the
   release, and a break of a Go signature needs a new major version.
2. **What counts as a break.** Removing or renaming a route, a method, a member, a query parameter, a header, an error code, an
   extension, a condition type or reason, an enum value, a command, a flag, a member of `-o json` output or an exit code;
   changing a type or making an optional member required; moving a route between `sync` and `async`; changing the HTTP status
   of an error code; refusing something that used to succeed; changing the format of the `ETag`; refusing a stored shape or a
   configuration key that a 1.x release wrote.
3. **What counts as an addition.** A new optional member of an answer, a new route, extension, enum value, condition type,
   reason or error code, and a new optional member of a request that a new extension introduces. **A client ignores a member
   it does not know and treats an enum value it does not know as "other"**: an unknown status is not final, and an unknown
   error code is an internal error (exit status 7). `TestClientReadsAnswersOfALaterServer` and
   `TestClientReadsUnknownEnumValues` check that for the client library.
4. **Removal.** A feature that goes in the next major release is announced under "Removal planned" in the
   [changelog](../CHANGELOG.md), and its route answers with the header `Deprecation: true`. It is removed in the next
   major release and not before.
5. **Extensions.** The list of an extension is append-only. An extension of the second list in [api.md](api.md#which-extensions-carry-the-1x-promise)
   may break with a new extension name; it moves to the first list in a minor release once it has a golden of its wire and a call
   in the previous-client test.
6. **Negotiation.** Capabilities are the extension names of `GET /api/v1`. `api_version` stays `1.0` for all of 1.x.
7. **Support window.** A client or command line of any 1.x works against a later 1.x daemon. A hub works with hosts of the
   same minor release; the claim it sends is frozen (see above).

## What the tests prove, and what they do not

The rules above are checked by these tests: every route with its method, status class and `sync` or `async` answer, the error
codes, the envelopes, the enum members, the conditions with their reasons, the extension lists, the wire of the requestor
claim, the command and flag tree with its exit codes, the machine output of the commands, the client library of the previous
release against the current server, and the state of every release from 0.8.0 on opened by the current code.

These gates are not built yet; until they are, a person reviews the change:

- a check of the Go API of the client library between two releases (`apidiff`);
- a check that a golden file that loses a line fails unless the major version changes;
- a replay of recorded hub and host conversations of an earlier release;
- a replay of an earlier release's command line against the current daemon;
- a golden of the names JailOps gives to pf anchors, tables and interfaces;
- the fixture of 0.9.0, which is made right after the release is tagged.

## A snapshot published by an older release

Stored state written by an older release is read by a newer daemon: shapes are migrated at load (`internal/upgradecheck`
opens the state of every release from 0.8.0 on with the current code). The other direction is refused, not guessed: a database
or a stored document of a newer version gives `version_skew`, and the backup receipt carries a format number that
a daemon which does not know it refuses (`unsupported format`).
