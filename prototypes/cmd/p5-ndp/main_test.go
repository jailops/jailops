package main

import "testing"

func TestCheckCmd(t *testing.T) {
	up, ja := "igb0", "2001:db8:1::5000"
	ok := [][]string{
		{"ifconfig", "igb0"},
		{"ifconfig", "-l"},
		{"ifconfig", "bridge", "create"},
		{"ifconfig", "bridge0", "name", "jopbr0"},
		{"ifconfig", "epair0b", "name", "jope0b"},
		{"ifconfig", "jopbr0", "addm", "jope0a"},
		{"ifconfig", "jopbr0", "addm", "igb0"}, // only with partD
		{"route", "add", "-inet6", ja + "/128", "-interface", "jopbr0"},
		{"route", "delete", "-inet6", ja + "/128"},
		{"route", "-n", "get", "-inet6", "default"},
		{"ndp", "-s", ja, "02:00:00:00:00:01", "proxy"},
		{"ndp", "-d", ja},
		{"jail", "-c", "name=jop5", "path=/", "vnet"},
		{"jail", "-r", "jop5"},
		{"jexec", "jop5", "ifconfig", "jope0b", "up"},
		{"sysctl", "net.inet6.ip6.forwarding=1"},
		{"pfctl", "-sr"},
	}
	for i, a := range ok {
		if err := checkCmd(a, up, ja, true); err != nil {
			t.Errorf("%v: %v", a, err)
		}
		_ = i
	}
	bad := [][]string{
		{"ifconfig", "igb0", "down"},
		{"ifconfig", "igb0", "inet6", "::1"},
		{"ifconfig", "bridge0", "destroy"},
		{"ifconfig", "jopbr0", "addm", "em1"},
		{"ifconfig", "bridge0", "name", "foo"},
		{"route", "add", "-inet6", "default", "fe80::1"},
		{"route", "delete", "-inet6", "default"},
		{"ndp", "-s", "2001:db8::1", "aa:bb:cc:dd:ee:ff", "proxy"},
		{"ndp", "-c"},
		{"jail", "-r", "web"},
		{"jexec", "web", "sh"},
		{"pfctl", "-f", "/etc/pf.conf"},
		{"pfctl", "-ef"},
		{"zfs", "destroy", "x"},
		{"rm", "-rf", "/"},
	}
	for _, a := range bad {
		if err := checkCmd(a, up, ja, true); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
	if checkCmd([]string{"ifconfig", "jopbr0", "addm", "igb0"}, up, ja, false) == nil {
		t.Error("uplink addm must need part D")
	}
}
