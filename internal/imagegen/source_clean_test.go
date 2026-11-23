package imagegen

import "testing"

func TestCleanOutput(t *testing.T) {
	in := "[26/44] Upgrading FreeBSD-x...\r\x1b[26/44] Extracting a: \x1b[1m50%\x1b[0m\r[26/44] Extracting a... done\npkg: Write error\r\n"
	want := "[26/44] Extracting a... done\npkg: Write error\n"
	if got := CleanOutput(in); got != want {
		t.Fatalf("%q", got)
	}
}
