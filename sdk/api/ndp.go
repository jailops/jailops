package api

import "time"

// NDPStatus is the reply of GET /api/v1/ndp: the neighbor discovery proxy of each uplink that has IPv6 proxy-mode
// networks, with what it knows about every address it holds or was asked for.
type NDPStatus struct {
	Uplinks []NDPUplink `json:"uplinks"`
	// Networks are the networks with an IPv6 prefix and what the proxy answers for on each (in proxy mode).
	Networks []NDPNetwork `json:"networks"`
}

// AnswerLimit6 is how many addresses of the IPv6 allocation range of a network the proxy answers for, counted from the
// first address, besides the address of every running jail.
const AnswerLimit6 = 1024

// NDPNetwork is the neighbor discovery proxy of one network.
type NDPNetwork struct {
	Name string `json:"name"`
	// Enabled is true while the network is in proxy mode.
	Enabled bool   `json:"enabled"`
	Uplink  string `json:"uplink,omitempty"`
	Prefix  string `json:"prefix"`
	Range   string `json:"range"`
	// Answers is the number of addresses of the range the proxy answers for, besides the address of every running
	// jail; Beyond is true when the range is larger than the limit and the addresses above it are answered only while
	// their jail runs.
	Answers int  `json:"answers"`
	Beyond  bool `json:"beyond,omitempty"`
}

// NDPUplink is the proxy of one uplink.
type NDPUplink struct {
	Uplink string `json:"uplink"`
	// Open is true while the capture of the uplink is open; Error says why it is not.
	Open  bool   `json:"open"`
	Error string `json:"error,omitempty"`
	// Reopens counts the times the capture was opened.
	Reopens   uint64 `json:"reopens"`
	Addresses int    `json:"addresses"`
	Answered  uint64 `json:"answered"`
	Announced uint64 `json:"announced"`
	// Dropped counts the solicitations that were not answered; Floods those dropped by a rate limit.
	Dropped      uint64       `json:"dropped"`
	Floods       uint64       `json:"floods"`
	InjectErrors uint64       `json:"inject_errors"`
	Addrs        []NDPAddress `json:"addrs,omitempty"`
}

// NDPAddress is what the proxy knows about one address: held or not, the solicitations it saw for it and what it did.
type NDPAddress struct {
	Address string `json:"address"`
	// Interface is the host end of the jail's epair the address is routed through.
	Interface string `json:"interface,omitempty"`
	// Held is true while the proxy answers for the address; false for an address that was asked for and is not held.
	Held         bool       `json:"held"`
	Since        *time.Time `json:"since,omitempty"`
	Seen         uint64     `json:"seen"`
	Answered     uint64     `json:"answered"`
	Dropped      uint64     `json:"dropped"`
	Announced    uint64     `json:"announced"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	LastFrom     string     `json:"last_from,omitempty"`
	LastVerdict  string     `json:"last_verdict,omitempty"`
	LastAnswer   *time.Time `json:"last_answer,omitempty"`
	LastAnnounce *time.Time `json:"last_announce,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// NDPEvents is the reply of GET /api/v1/ndp/events?after=N: the recent events of the proxies, oldest first, and the
// sequence number to ask for next.
type NDPEvents struct {
	Events []NDPEvent `json:"events"`
	Last   uint64     `json:"last"`
}

// NDPEvent is one solicitation the proxy saw or one announcement it sent.
type NDPEvent struct {
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	Uplink  string    `json:"uplink"`
	Kind    string    `json:"kind"`
	Target  string    `json:"target"`
	From    string    `json:"from,omitempty"`
	Verdict string    `json:"verdict"`
}
