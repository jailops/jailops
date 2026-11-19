package top

import (
	"context"
	"testing"
	"time"
)

// the output of FreeBSD 15.1 as it printed it
const netstatOut = `Name     Mtu Network   Address               Ipkts Ierrs Idrop       Ibytes     Opkts Oerrs       Obytes  Coll
vtnet0  1500 <Link#1>  52:54:00:12:34:56  35846560     0     0  51903308722  18567439     0   1149726801     0
lo0    16384 <Link#2>  lo0                 1891101     0     0   5896706299   1891101     0   5896706299     0
jobd871f7f3f00a  1500 <Link#7>  02:a1:b2:c3:d4:e5      10     0     0         2048        20     0         4096     0
`

const sysctlOut = `vm.loadavg={ 0.76 0.61 0.35 }
kern.cp_time=6476239 12 9810900 122234 131783895
hw.ncpu=2
hw.physmem=4246441984
vm.stats.vm.v_wire_count=379101
vm.stats.vm.v_active_count=26614
vm.stats.vm.v_page_size=4096
kstat.zfs.misc.arcstats.size=36248
`

func TestParseNetstat(t *testing.T) {
	m := ParseNetstat(netstatOut)
	if len(m) != 3 {
		t.Fatalf("%v", m)
	}
	if c := m["vtnet0"]; c.InBytes != 51903308722 || c.OutBytes != 1149726801 {
		t.Fatalf("%+v", c)
	}
	// an interface without a hardware address keeps its columns: the numbers are read from the end
	if c := m["lo0"]; c.InBytes != 5896706299 || c.OutBytes != 5896706299 {
		t.Fatalf("%+v", c)
	}
	if c := m["jobd871f7f3f00a"]; c.InBytes != 2048 || c.OutBytes != 4096 {
		t.Fatalf("%+v", c)
	}
	if got := ParseNetstat("Name Mtu\nvtnet0 1500 <Link#1> x notanumber\n"); len(got) != 0 {
		t.Fatalf("a row that is not numbers is skipped: %v", got)
	}
}

func TestParseSysctl(t *testing.T) {
	at := time.Unix(100, 0)
	h, err := ParseSysctl(sysctlOut, at)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HaveLoad || h.Load != [3]float64{0.76, 0.61, 0.35} || h.NCPU != 2 || h.PhysMem != 4246441984 {
		t.Fatalf("%+v", h)
	}
	if !h.HaveCPU || h.CPUTimes != [5]uint64{6476239, 12, 9810900, 122234, 131783895} {
		t.Fatalf("%+v", h.CPUTimes)
	}
	if !h.HaveMem || h.Wired != 379101 || h.Active != 26614 || h.PageSize != 4096 || !h.HaveARC || h.ARC != 36248 {
		t.Fatalf("%+v", h)
	}
	// a kernel that lacks a name leaves a gap and shifts nothing
	h, err = ParseSysctl("vm.loadavg={ 1.00 2.00 3.00 }\nhw.ncpu=4\n", at)
	if err != nil || !h.HaveLoad || h.NCPU != 4 || h.HaveCPU || h.HaveMem || h.HaveARC {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := ParseSysctl("sysctl: unknown oid\n", at); err == nil {
		t.Fatal("nothing usable is an error")
	}
	// malformed values are gaps as well
	h, _ = ParseSysctl("vm.loadavg={ x y z }\nkern.cp_time=1 2 3\nhw.ncpu=2\n", at)
	if h.HaveLoad || h.HaveCPU || h.NCPU != 2 {
		t.Fatalf("%+v", h)
	}
}

func TestCPUBusy(t *testing.T) {
	prev := Host{HaveCPU: true, CPUTimes: [5]uint64{100, 0, 100, 0, 800}}
	cur := Host{HaveCPU: true, CPUTimes: [5]uint64{150, 0, 150, 0, 900}}
	if v, ok := CPUBusy(prev, cur); !ok || v != 50 {
		t.Fatalf("%v %v", v, ok)
	}
	if _, ok := CPUBusy(prev, prev); ok {
		t.Fatal("no ticks passed: not known")
	}
	back := Host{HaveCPU: true, CPUTimes: [5]uint64{10, 0, 10, 0, 10}}
	if _, ok := CPUBusy(prev, back); ok {
		t.Fatal("counters that went backwards: not known")
	}
	if _, ok := CPUBusy(Host{}, cur); ok {
		t.Fatal("no previous reading: not known")
	}
}

func TestRate(t *testing.T) {
	if v, ok := Rate(1000, 3000, 2*time.Second); !ok || v != 1000 {
		t.Fatalf("%v %v", v, ok)
	}
	if _, ok := Rate(3000, 1000, time.Second); ok {
		t.Fatal("an interface that was recreated has no rate")
	}
	if _, ok := Rate(1, 2, 0); ok {
		t.Fatal("no time passed")
	}
}

func TestParsePS(t *testing.T) {
	n, cpu, rss := ParsePS(" 0.0  1024\n12.5 20480\n  0.3   512\n\ngarbage\n")
	if n != 3 || cpu != 12.8 || rss != (1024+20480+512)*1024 {
		t.Fatalf("%d %v %d", n, cpu, rss)
	}
	if n, _, _ := ParsePS(""); n != 0 {
		t.Fatal("a jail with no process")
	}
}

func TestExecDirectRefusesATool(t *testing.T) {
	if _, err := ExecDirect(context.Background(), "rm", "-rf", "/"); err == nil {
		t.Fatal("only the tools it reads run")
	}
}
