package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// topSleep waits between two frames (tests replace it).
var topSleep = sleepCtx

// sleepCtx sleeps d or until the context ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

const (
	// topMinInterval is the shortest time between two frames: the server measures at most once a second.
	topMinInterval = time.Second
	// topPrime is how long the first frame waits for the second, which is the one that has rates: a little more than
	// the second the server reuses a frame for.
	topPrime = 1100 * time.Millisecond
)

var topSorts = []string{"cpu", "mem", "disk", "net", "name"}

func (a *app) topCmd() *cobra.Command {
	var sortBy string
	var interval time.Duration
	var once bool
	var selectors []string
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Watch what the running jails and the host use, live",
		Long: `Show the running jails of this host with what each uses now, and the host: load, CPU, memory, the ZFS
cache and the pool. On a terminal the table redraws every interval; press c, m, d or n to sort by CPU, memory,
disk or network, a for the name, and q to quit.

A jail shows CPU in percent of one core (100 is one full core), resident memory next to the memory limit (the limit
is on the memory the jail reserves, not on the resident memory), the processes, the space its datasets take next to
its disk limit, the network it receives and sends in bytes per second, and how long it has run. A number that could
not be measured is n/a, and a line under the table says why. Without racct (kern.racct.enable=1 in /boot/loader.conf,
which takes a reboot) CPU and memory are read from the process list and are approximate.

You see the running jails of every project you may use; --project shows one. The host line is for a global identity.
With --once (or when the output is not a terminal) one frame is printed after a second, the time the rates need. -o
json and -o yaml print a frame every interval until you stop the command.

The server measures when it is asked and shares one reading between everyone who watches, so a host nobody looks at
does no work.`,
		Example: `jailops top
jailops top --sort mem
jailops top -l role=web --interval 5s
jailops top --once
jailops top -o json | jq '.jails[] | {name, cpu_percent}'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !slices.Contains(topSorts, sortBy) {
				return usagef("--sort is one of %s", strings.Join(topSorts, ", "))
			}
			if interval < topMinInterval {
				return usagef("the interval is at least %s", topMinInterval)
			}
			var labels []string
			for _, s := range selectors {
				labels = append(labels, strings.Split(s, ",")...)
			}
			opts, err := labelOpts(labels)
			if err != nil {
				return err
			}
			if !cmd.Flag("project").Changed {
				opts = append(opts, sdk.AllProjects()) // the whole host unless --project names one
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fetch := func() (api.Top, error) { return c.Top(ctx, opts...) }

			live := !once && a.format == fmtTable && a.env.IsTTY(a.env.Stdout)
			switch {
			case live:
				return a.topLive(ctx, fetch, sortBy, interval)
			case once || a.format == fmtTable || a.format == fmtCSV || a.format == fmtName:
				f, err := a.topFrame(ctx, fetch)
				if err != nil {
					return err
				}
				return a.topEmit(f, sortBy)
			}
			return a.topStream(ctx, fetch, interval)
		},
	}
	f := cmd.Flags()
	f.StringVar(&sortBy, "sort", "cpu", "sort by "+strings.Join(topSorts, ", ")+" (CPU, memory, disk and network biggest first); while it runs c, m, d, n and a change it")
	f.DurationVar(&interval, "interval", 2*time.Second, "time between two frames, at least 1s")
	f.BoolVar(&once, "once", false, "print one frame and exit")
	f.StringArrayVarP(&selectors, "selector", "l", nil, "show the jails carrying these labels, key=value (comma separated or repeated)")
	enumFlag(cmd, "sort", topSorts...)
	return cmd
}

// topFrame measures the frame that has rates: the server compares a frame with the one before it, so the first call
// only primes it and the frame that is shown is the second, a second later.
func (a *app) topFrame(ctx context.Context, fetch func() (api.Top, error)) (api.Top, error) {
	if _, err := fetch(); err != nil {
		return api.Top{}, err
	}
	if err := topSleep(ctx, topPrime); err != nil {
		return api.Top{}, err
	}
	return fetch()
}

// topEmit prints one frame in the chosen format: the table is the text of the terminal view without the colours.
func (a *app) topEmit(f api.Top, sortBy string) error {
	switch a.format {
	case fmtJSON, fmtYAML:
		return a.emit(f, nil, nil, nil)
	case fmtCSV:
		return a.emit(f, nil, func() ([]string, [][]string) {
			return topTable(f, sortBy)
		}, nil)
	case fmtName:
		return a.emit(f, nil, nil, func() []string {
			var n []string
			for _, j := range topSorted(f.Jails, sortBy) {
				n = append(n, j.Name)
			}
			return n
		})
	}
	var b bytes.Buffer
	for _, l := range topLines(f, sortBy, 0, 0, false) {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	_, err := a.env.Stdout.Write(b.Bytes())
	return err
}

// topStream prints a frame every interval, in JSON or YAML, until the command is stopped. The first frame is
// measured before anything is printed, so every printed frame has its rates.
func (a *app) topStream(ctx context.Context, fetch func() (api.Top, error), interval time.Duration) error {
	if _, err := fetch(); err != nil {
		return err
	}
	wait := topPrime
	for n := 0; ; n++ {
		if err := topSleep(ctx, wait); err != nil {
			return nil // stopped
		}
		wait = interval
		f, err := fetch()
		if err != nil {
			return err
		}
		if n > 0 && a.format == fmtYAML {
			fprintln(a.env.Stdout, "---")
		}
		if err := a.topEmit(f, "name"); err != nil {
			return err
		}
	}
}

// topLive redraws the table on the terminal until q or Ctrl-C.
func (a *app) topLive(ctx context.Context, fetch func() (api.Top, error), sortBy string, interval time.Duration) error {
	out := a.env.Stdout
	keys := make(chan byte, 8)
	if a.env.IsTTY(a.env.Stdin) {
		restore, err := a.env.RawTerminal(a.env.Stdin)
		if err != nil {
			return err
		}
		defer restore()
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := a.env.Stdin.Read(buf)
				if n > 0 {
					select {
					case keys <- buf[0]:
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
	// the alternate screen keeps what the shell showed; the cursor is hidden while the table is drawn
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")

	var last api.Top
	var lastErr error
	draw := func() {
		rows, cols, ok := a.env.TermSize(out)
		if !ok {
			rows, cols = 0, 0
		}
		lines := topLines(last, sortBy, int(rows), int(cols), true)
		if lastErr != nil {
			lines = append(lines, "", "the last reading failed: "+lastErr.Error())
		}
		var b bytes.Buffer
		b.WriteString("\x1b[H")
		for _, l := range lines {
			b.WriteString(l)
			b.WriteString("\x1b[K\r\n")
		}
		b.WriteString("\x1b[J")
		_, _ = out.Write(b.Bytes())
	}
	fetchAndDraw := func() error {
		f, err := fetch()
		var ae *api.Error
		if err != nil && (errors.As(err, &ae) && (ae.Code == api.CodeUnsupported || ae.Code == api.CodeUnauthorized || ae.Code == api.CodeForbidden)) {
			return err
		}
		if err == nil {
			last, lastErr = f, nil
		} else {
			lastErr = err
		}
		draw()
		return nil
	}
	if err := fetchAndDraw(); err != nil {
		return err
	}
	wait := topPrime
	for {
		// the next frame is due after wait; a key in between sorts the frame that is on the screen
		wctx, cancel := context.WithCancel(ctx)
		tick := make(chan error, 1)
		go func() { tick <- topSleep(wctx, wait) }()
	inner:
		for {
			select {
			case <-ctx.Done():
				cancel()
				return nil
			case k := <-keys:
				switch k {
				case 'q', 'Q', 3, 4: // q, Ctrl-C, Ctrl-D
					cancel()
					return nil
				case 'c':
					sortBy = "cpu"
				case 'm':
					sortBy = "mem"
				case 'd':
					sortBy = "disk"
				case 'n':
					sortBy = "net"
				case 'a':
					sortBy = "name"
				default:
					continue inner
				}
				draw()
			case err := <-tick:
				cancel()
				if err != nil {
					return nil
				}
				break inner
			}
		}
		wait = interval
		if err := fetchAndDraw(); err != nil {
			return err
		}
	}
}

// ---- the table ----

// topSorted returns the jails in the order of the key: CPU, memory, disk and network biggest first, the name
// alphabetically; a jail without the number comes last.
func topSorted(jails []api.TopJail, key string) []api.TopJail {
	out := slices.Clone(jails)
	num := func(j api.TopJail) (float64, bool) {
		switch key {
		case "cpu":
			if j.CPUPercent != nil {
				return *j.CPUPercent, true
			}
		case "mem":
			if j.MemoryBytes != nil {
				return float64(*j.MemoryBytes), true
			}
		case "disk":
			if j.DiskBytes != nil {
				return float64(*j.DiskBytes), true
			}
		case "net":
			if j.NetInBytesPerSecond != nil || j.NetOutBytesPerSecond != nil {
				var s int64
				if j.NetInBytesPerSecond != nil {
					s += *j.NetInBytesPerSecond
				}
				if j.NetOutBytesPerSecond != nil {
					s += *j.NetOutBytesPerSecond
				}
				return float64(s), true
			}
		}
		return 0, false
	}
	sort.SliceStable(out, func(i, j int) bool {
		if key != "name" {
			vi, oki := num(out[i])
			vj, okj := num(out[j])
			switch {
			case oki && !okj:
				return true
			case !oki && okj:
				return false
			case oki && okj && vi != vj:
				return vi > vj
			}
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// topShort writes a size in the shortest binary form: 388M, 1.5G, 12K.
func topShort(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	f, exp := float64(n)/unit, 0
	for f >= unit && exp < 4 {
		f /= unit
		exp++
	}
	u := "KMGTP"[exp]
	if f < 10 {
		return fmt.Sprintf("%.1f%c", f, u)
	}
	return fmt.Sprintf("%.0f%c", f, u)
}

func topPtr[T any](p *T, f func(T) string) string {
	if p == nil {
		return "n/a"
	}
	return f(*p)
}

// topTable is the table of the jails in the order of the key; the project column is there when the frame holds more
// than one project.
func topTable(f api.Top, sortBy string) ([]string, [][]string) {
	projects := map[string]bool{}
	for _, j := range f.Jails {
		projects[j.Project] = true
	}
	multi := len(projects) > 1
	head := []string{"JAIL", "CPU%", "MEM", "PROCS", "DISK", "NET IN", "NET OUT", "UPTIME"}
	if multi {
		head = append([]string{"PROJECT"}, head...)
	}
	var rows [][]string
	for _, j := range topSorted(f.Jails, sortBy) {
		mem := topPtr(j.MemoryBytes, topShort)
		if j.MemoryLimitBytes > 0 && j.MemoryBytes != nil {
			mem += "/" + topShort(j.MemoryLimitBytes)
		}
		cpu := topPtr(j.CPUPercent, func(v float64) string { return fmt.Sprintf("%.1f", v) })
		if j.CPULimitPercent > 0 && j.CPUPercent != nil {
			cpu += fmt.Sprintf("/%d", j.CPULimitPercent)
		}
		disk := topPtr(j.DiskBytes, topShort)
		if j.DiskQuotaBytes > 0 && j.DiskBytes != nil {
			disk += "/" + topShort(j.DiskQuotaBytes)
		}
		rate := func(v *int64) string { return topPtr(v, func(n int64) string { return topShort(n) + "/s" }) }
		up := "-"
		if j.UptimeSeconds > 0 {
			up = humanSeconds(j.UptimeSeconds)
		}
		if j.Approximate {
			cpu, mem = "~"+cpu, "~"+mem
		}
		row := []string{j.Name, cpu, mem, topPtr(j.Processes, func(n int) string { return fmt.Sprint(n) }), disk,
			rate(j.NetInBytesPerSecond), rate(j.NetOutBytesPerSecond), up}
		if multi {
			row = append([]string{j.Project}, row...)
		}
		rows = append(rows, row)
	}
	return head, rows
}

// topLines is the whole frame as text: the host lines, the table, the notes. rows and cols are the size of the
// terminal (0 for no limit); color adds the bold of the headers and the key legend of the live view.
func topLines(f api.Top, sortBy string, rows, cols int, color bool) []string {
	var out []string
	clock := f.MeasuredAt.Local().Format("15:04:05")
	if h := f.Host; h != nil {
		cpu := "n/a"
		if h.CPUPercent != nil {
			cpu = fmt.Sprintf("%.1f%%", *h.CPUPercent)
		}
		out = append(out, fmt.Sprintf("jailops top - %s   load %.2f %.2f %.2f   cpu %s of %d   %d running jails", clock, h.Load1, h.Load5, h.Load15, cpu, h.CPUs, h.RunningJails))
		mem := "memory n/a"
		if h.MemoryUsed != nil {
			mem = "memory " + topShort(int64(*h.MemoryUsed))
			if h.MemoryTotal > 0 {
				mem += " of " + topShort(int64(h.MemoryTotal))
			}
		}
		if h.ARCBytes != nil {
			mem += "   zfs cache " + topShort(int64(*h.ARCBytes))
		}
		if h.PoolSize != nil && h.PoolAllocated != nil {
			mem += fmt.Sprintf("   pool %s %s of %s", h.Pool, topShort(int64(*h.PoolAllocated)), topShort(int64(*h.PoolSize)))
			if h.PoolFree != nil {
				mem += " (" + topShort(int64(*h.PoolFree)) + " free)"
			}
		}
		out = append(out, mem, "")
	} else {
		out = append(out, fmt.Sprintf("jailops top - %s   %d running jails", clock, len(f.Jails)), "")
	}
	head, body := topTable(f, sortBy)
	if len(body) == 0 {
		out = append(out, "no running jail")
	} else {
		var b bytes.Buffer
		topAlign(&b, head, body)
		lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
		if color {
			lines[0] = "\x1b[1m" + lines[0] + "\x1b[0m"
		}
		out = append(out, lines...)
	}
	if len(f.Notes) > 0 {
		out = append(out, "")
		for _, n := range f.Notes {
			out = append(out, n)
		}
	}
	if color {
		out = append(out, "", "c cpu  m mem  d disk  n net  a name  q quit   sorted by "+sortBy)
	}
	if rows > 0 && len(out) > rows {
		// the host lines, the head and the notes stay; the jails that do not fit are counted
		keep := rows - 1
		hidden := len(out) - keep
		out = append(out[:keep:keep], fmt.Sprintf("... %d more lines (make the window taller)", hidden))
	}
	if cols > 0 {
		for i, l := range out {
			out[i] = topClip(l, cols)
		}
	}
	return out
}

// topAlign writes a table with the columns padded to their widest cell.
func topAlign(b *bytes.Buffer, head []string, rows [][]string) {
	w := make([]int, len(head))
	for i, h := range head {
		w[i] = len([]rune(h))
	}
	for _, r := range rows {
		for i, c := range r {
			w[i] = max(w[i], len([]rune(c)))
		}
	}
	line := func(r []string) {
		var parts []string
		for i, c := range r {
			parts = append(parts, c+strings.Repeat(" ", w[i]-len([]rune(c))))
		}
		b.WriteString(strings.TrimRight(strings.Join(parts, "  "), " "))
		b.WriteByte('\n')
	}
	line(head)
	for _, r := range rows {
		line(r)
	}
}

// topClip cuts a line to cols columns, escape sequences aside.
func topClip(l string, cols int) string {
	if cols <= 0 {
		return l
	}
	n := 0
	var b strings.Builder
	in := false
	for _, r := range l {
		switch {
		case r == 0x1b:
			in = true
			b.WriteRune(r)
		case in:
			b.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				in = false
			}
		default:
			if n >= cols {
				return b.String() + "\x1b[0m"
			}
			n++
			b.WriteRune(r)
		}
	}
	return b.String()
}
