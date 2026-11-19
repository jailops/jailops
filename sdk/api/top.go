package api

import "time"

// Top is one frame of the live usage of the running jails of this host and of the host itself: the answer of
// GET /api/v1/top (extension top). A frame is measured when it is asked for and never stored; the rates in it are
// computed against the frame measured before it, so the first frame after a quiet period has none (a field that
// cannot be measured is absent, never zero).
type Top struct {
	// MeasuredAt is when the frame was taken.
	MeasuredAt time.Time `json:"measured_at"`
	// IntervalSeconds is the time between the sample the rates compare and this one; zero when the frame has no
	// rates (nothing measured before it).
	IntervalSeconds float64 `json:"interval_seconds,omitempty"`
	// Host is the host's own load, memory and pool. Only a global identity sees it.
	Host *TopHost `json:"host,omitempty"`
	// Jails are the running jails the identity may see, by project and name.
	Jails []TopJail `json:"jails"`
	// Notes say what is approximate or missing in this frame ("racct is off: ...").
	Notes []string `json:"notes,omitempty"`
}

// TopHost is the host header of a frame.
type TopHost struct {
	// Load1, Load5 and Load15 are the load averages.
	Load1  float64 `json:"load_1"`
	Load5  float64 `json:"load_5"`
	Load15 float64 `json:"load_15"`
	// CPUs is the number of CPUs.
	CPUs int `json:"cpus"`
	// CPUPercent is how busy the CPUs were between the two samples, 0 to 100 for all CPUs together.
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
	// MemoryTotal is the physical memory; MemoryUsed what the kernel and the processes hold (wired and active), the
	// ZFS cache included, which ARCBytes shows on its own.
	MemoryTotal uint64  `json:"memory_total_bytes,omitempty"`
	MemoryUsed  *uint64 `json:"memory_used_bytes,omitempty"`
	ARCBytes    *uint64 `json:"arc_bytes,omitempty"`
	// Pool is the pool that holds the JailOps datasets.
	Pool          string  `json:"pool,omitempty"`
	PoolSize      *uint64 `json:"pool_size_bytes,omitempty"`
	PoolAllocated *uint64 `json:"pool_allocated_bytes,omitempty"`
	PoolFree      *uint64 `json:"pool_free_bytes,omitempty"`
	// RunningJails is the number of running jails on the host, in every project.
	RunningJails int `json:"running_jails"`
}

// TopJail is one running jail of a frame. A field that could not be measured is absent: the reason is in the
// frame's notes.
type TopJail struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	// UptimeSeconds is the time since the jail started.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
	// CPUPercent is the CPU the jail uses in percent of one core (100 is one full core, 250 two and a half).
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
	// CPULimitPercent is the limit of resources.cpu in the same unit; zero without a limit.
	CPULimitPercent int `json:"cpu_limit_percent,omitempty"`
	// MemoryBytes is the resident memory of the jail's processes; MemoryLimitBytes the limit of resources.memory, which
	// limits the memory the jail reserves, not the resident memory; zero without a limit.
	MemoryBytes      *int64 `json:"memory_bytes,omitempty"`
	MemoryLimitBytes int64  `json:"memory_limit_bytes,omitempty"`
	// Processes is the number of processes.
	Processes *int `json:"processes,omitempty"`
	// DiskBytes is what the jail's own datasets take on the pool; DiskQuotaBytes the limit of resources.disk, zero
	// without one.
	DiskBytes      *int64 `json:"disk_bytes,omitempty"`
	DiskQuotaBytes int64  `json:"disk_quota_bytes,omitempty"`
	// NetInBytesPerSecond is what the jail receives, NetOutBytesPerSecond what it sends, over all its interfaces.
	NetInBytesPerSecond  *int64 `json:"net_in_bytes_per_second,omitempty"`
	NetOutBytesPerSecond *int64 `json:"net_out_bytes_per_second,omitempty"`
	// Approximate is true when the CPU and memory numbers come from the process list because racct is off.
	Approximate bool `json:"approximate,omitempty"`
}
