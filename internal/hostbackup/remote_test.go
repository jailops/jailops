package hostbackup

import "testing"

func TestNodeAddressAcceptsTheDocumentedForm(t *testing.T) {
	for in, want := range map[string]string{"https://vault.example.net:8444": "vault.example.net:8444", "https://127.0.0.1:8444/": "127.0.0.1:8444", "vault:8444": "vault:8444", " https://[::1]:8444 ": "[::1]:8444"} {
		if got := nodeAddress(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
