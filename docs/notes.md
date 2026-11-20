# Notes

Every jail has two texts about it. The **comment** is one line, what the jail is for: lists show it. The **notes** are
the rest: what the jail does, how it was set up, who to ask, the runbook for a bad night. They are `spec.notes`, empty
until somebody writes them.

Networks, volumes, projects, storage, firewall policies, address sets, images and profiles have notes too, with the same
rules.

## The command line

```sh
jailops jail notes web                        # print them; nothing when there are none
jailops jail notes edit web                   # open them in your editor
jailops jail notes set web --file runbook.md  # replace them with a file (- reads standard input)
jailops jail notes clear web                  # remove them
```

`notes edit` opens `$JAILOPS_EDITOR`, `$VISUAL` or `$EDITOR` (`vi` when none is set) on a file that holds the notes
alone. Saving the file unchanged changes nothing; saving it empty removes the notes. The change carries the version you
started from: when somebody changed the jail while your editor was open, the server answers `precondition_failed`,
nothing is overwritten, and the error names the file that keeps your text, ready for `notes set --file`.

`jailops network notes`, `jailops volume notes` and `jailops project notes` work the same way. The other kinds take
`notes` as a key of `edit` and `apply -f`:

```yaml
kind: FirewallPolicy
metadata: {name: web-access, project: shop}
spec:
  comment: who reaches the shop
  notes: |
    Opened for the payment provider's callbacks.
    Ask the ops team before removing a rule.
  rules: [...]
```

`jailops jail show web` prints the first line of the notes and how many lines follow; `-o json` and `-o yaml` carry the
whole text in `spec.notes`, and `jailops jail notes web -o json` answers `{"notes": "..."}`. Lists show the comment,
never the notes.

## The rules

- At most 64 KiB of UTF-8 text. Line feeds and tabs are allowed; carriage returns, escape sequences, other control
  characters and characters that reverse or hide text are refused with `invalid`. The command line turns CRLF line ends
  into LF and drops blank space at the end before it sends the text.
- A change of the notes is an update of the object: a new generation, the `If-Match` check, a `jail-updated` event
  and a line in the audit log. It needs the **admin** permission in the jail's project, like the comment and the labels.
- Notes are not rendered into `jail.conf`, the fstab or a firewall anchor: a change never needs a restart, and a change
  of a jail's notes does not wake the reconcile loop.
- Notes are part of the spec, so they go wherever the spec goes: `jail export` and `jail import`, backups and restores,
  `jail copy`, a rename, and the documents of a fleet repository (`notes:` in the spec of any of the kinds above).

## In the console

The jail page has a **Notes** tab; the pages of volumes and projects have a Notes panel, and so do the cards of
networks, storage and images and the editors of firewall policies and address sets (the notes of a profile are
changed with `jailops profile edit`). The panel shows the text with a small set of
Markdown: headings, lists, code blocks and inline code, bold, italics and links. Links go to `http`, `https` and
`mailto` addresses only; anything else, HTML included, is shown as the text it is, and images are never loaded.
**Edit** turns the panel into a text box: Ctrl-Enter (Cmd-Enter on a Mac) saves, Esc cancels. When somebody else saved
in the meantime, the console says so and keeps your text. The search of the console finds words in the notes of jails, networks, storage, images and projects.
