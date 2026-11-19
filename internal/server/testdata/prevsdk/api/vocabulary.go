package api

// The vocabulary of the wire that is not a Go type of its own. A client treats a value it does not know as "other":
// new members are added in a minor release and never removed or renamed (docs/api.md, "What a client does").

// Enums lists the members of the string fields that have a fixed set of values, by "Type.json_field". The fields that
// have a Go type of their own (OperationStatus, PowerState, ...) are listed by their constants.
var Enums = map[string][]string{
	"BackupKeyStatus.status":         {"none", "present", "escrowed"},
	"Condition.status":               {ConditionTrue, ConditionFalse, ConditionUnknown},
	"DiffEntry.kind":                 {"added", "modified", "removed", "renamed"},
	"FirewallPolicySpec.default_in":  {"allow", "deny"},
	"FirewallPolicySpec.default_out": {"allow", "deny"},
	"FirewallRule.action":            {"allow", "deny"},
	"FirewallRule.direction":         {"in", "out"},
	"FirewallRule.proto":             {"tcp", "udp", "icmp", "icmp6"},
	"FirewallTestResult.verdict":     {TestPass, TestBlock, TestUnknown},
	"ImageSource.type":               {"pkgbase", "minirootfs", "debootstrap"},
	"JailAction.action":              {"start", "stop", "restart"},
	"JailReach.via":                  {"uplink", "forward", "none"},
	"ServerInfo.auth":                {"trusted", "untrusted"},
	"VolumeStatus.state":             {"ok", "lost"},
	"VolumeSpec.on_loss":             {VolumeOnLossNone, VolumeOnLossStop},
}

// ConditionSpec says which reasons a condition of one kind of object has. The reasons are the reading of the
// condition for a program; the message is the sentence for people and has no fixed wording.
type ConditionSpec struct {
	Kind    string
	Type    string
	Reasons []string
}

// Conditions is every condition an object reports, by kind and type.
var Conditions = []ConditionSpec{
	{"Jail", "Autostart", []string{"DependencyFailed", "Locked", "NotStarted", "StartFailed", "Started"}},
	{"Jail", ConditionFirewallApplied, []string{"Applied", "NotLoaded", "PFNotApplying", "StatesPending", "Unverified"}},
	{"Jail", "BasePackages", []string{"OutOfStep"}},
	{"Jail", "Healthy", []string{"Degraded"}},
	{"Jail", "InSync", []string{"Drift", "InSync"}},
	{"Jail", "Limits", []string{"NotEnforced"}},
	{"Jail", "Ready", []string{"Invalid", "Running", "Stopped"}},
	{"Jail", "ShellSetup", []string{"Failed", ShellInstalling}},
	{"Network", "NDPFlood", []string{"Flooded"}},
	{"Network", "NDPProxy", []string{"Answering", "BadRange", "NoCapture", "StartFailed", "Starting", "Stopped", "SyncFailed", "Unsupported"}},
	{"Network", "Ready", []string{"ApplyFailed", "Applied", "Invalid", "Precondition"}},
	{"Project", "Host:<host>", []string{"Applied", "ApplyFailed", "Conflict", "Forbidden", "MissingNetwork", "MissingStorage", "Offline", "UnknownHost"}},
	{"Storage", "Ready", []string{"DatasetMissing", "NoZFS", "Online", "PoolMissing", "PoolUnhealthy", "PoolsUnreadable"}},
	{"WireGuard", "Ready", []string{"ApplyFailed", "Disabled", "Up"}},
}
