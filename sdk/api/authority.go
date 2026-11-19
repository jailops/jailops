package api

import (
	"sort"
	"strings"
)

// Authority says, for one kind of resource, who owns it and where it is the truth. It is the single source of the
// table "Resource authority" in docs/design.md (a test keeps the two equal) and of the rule that every kind has an
// owner.
type Authority struct {
	// Scope is the Scope the objects of the kind carry. Empty means the kind is not stored on a host: it is a
	// document of a fleet repository or a record that is built when it is asked for.
	Scope Scope
	// Owner is who the object belongs to: the host that stores it, a project of that host, or a document of the
	// fleet repository.
	Owner string
	// Writer is who may create, change and delete it.
	Writer string
	// Lives is where the truth is kept.
	Lives string
}

const (
	ownerHost    = "this host"
	ownerProject = "a project of this host"
	ownerRepo    = "the fleet repository"

	writerGlobal  = "global administrator"
	writerProject = "project administrator"
	writerOperate = "project operator"
	writerDaemon  = "the daemon (no API write)"

	livesState = "state files of the host"
	livesJail  = "dataset of the jail"
	livesZFS   = "ZFS on the host"
	livesDB    = "SQLite of the host"
	livesRepo  = "the fleet repository (not stored on a host)"
)

// Authorities lists every Kind. The scope `fleet` means that the hub and the fleet repository may manage the object
// by one definition; it does not mean shared state: each host keeps its own copy and is authoritative for it. Shared
// authority (a replicated log) exists only in cluster mode, which is not built.
var Authorities = map[Kind]Authority{
	KindJail:            {ScopeHost, ownerProject, writerOperate, livesJail},
	KindJailSnapshot:    {ScopeHost, ownerProject, writerOperate, livesZFS},
	KindVolume:          {ScopeHost, ownerProject, writerProject, livesState + " and the volume's dataset"},
	KindAppAccess:       {ScopeHost, ownerProject, writerProject, livesState},
	KindProject:         {ScopeHost, ownerHost, writerGlobal, livesState},
	KindStorage:         {ScopeHost, ownerHost, writerGlobal, livesState},
	KindLogForward:      {ScopeHost, ownerHost, writerGlobal, livesState},
	KindImage:           {ScopeFleet, ownerHost, writerGlobal, livesState + " and ZFS"},
	KindImageGeneration: {ScopeHost, ownerHost, writerDaemon, livesZFS},
	KindNetwork:         {ScopeFleet, ownerHost, writerGlobal, livesState},
	KindWireGuard:       {ScopeFleet, ownerHost, writerGlobal, livesState},
	KindFirewallPolicy:  {ScopeFleet, ownerProject, writerProject, livesState},
	KindAddressSet:      {ScopeFleet, ownerProject, writerProject, livesState},
	KindProfile:         {ScopeFleet, ownerProject, writerProject, livesState},
	KindBackupPolicy:    {ScopeFleet, ownerProject, writerProject, livesState},
	KindBackupTarget:    {ScopeFleet, ownerHost, writerGlobal, livesState},
	KindBackup:          {ScopeHost, ownerProject, writerDaemon, "the backup node"},
	KindRemote:          {ScopeFleet, ownerHost, writerGlobal, livesState},
	KindCertificate:     {ScopeFleet, ownerHost, writerGlobal, livesState},
	KindUser:            {ScopeFleet, ownerHost, writerGlobal, livesDB},
	KindToken:           {ScopeFleet, ownerHost, writerGlobal + " or the token's user", livesDB},
	KindOperation:       {ScopeHost, ownerProject, writerDaemon, livesDB},
	KindHostIdentity:    {ScopeHost, ownerHost, writerDaemon, livesDB},
	KindHostFirewall:    {ScopeHost, ownerHost, writerGlobal, "files of the host"},
	KindHostCertificate: {ScopeHost, ownerHost, writerGlobal, "files of the host"},
	KindHost:            {"", ownerRepo, writerGlobal, livesRepo},
	KindHostGroup:       {"", ownerRepo, writerGlobal, livesRepo},
	KindRollout:         {"", ownerRepo, writerGlobal, livesRepo},
	KindJailTemplate:    {"", ownerRepo, writerGlobal, livesRepo},
}

// AuthorityTable renders Authorities as the Markdown table of docs/design.md, sorted by kind.
func AuthorityTable() string {
	kinds := make([]string, 0, len(Authorities))
	for k := range Authorities {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	var b strings.Builder
	b.WriteString("| Kind | Scope | Owner | Who may write | Where it lives |\n|---|---|---|---|---|\n")
	for _, k := range kinds {
		a := Authorities[Kind(k)]
		sc := string(a.Scope)
		if sc == "" {
			sc = "none"
		}
		b.WriteString("| `" + k + "` | `" + sc + "` | " + a.Owner + " | " + a.Writer + " | " + a.Lives + " |\n")
	}
	return b.String()
}
