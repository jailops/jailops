package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The serialised form of a Command is what a replicated log would keep for good, so it is frozen: one golden file per
// kind of command under testdata/command, written with the fields in the order of the struct. A change of the wire form
// that is meant is accepted with
//
//	go test ./internal/state -run Freeze -freeze-update
//
// and raises CommandVersion when the meaning of an existing field changes (a replica refuses a version it does not know,
// see TestCommandDecode).
var freezeUpdate = flag.Bool("freeze-update", false, "rewrite the golden files of the command wire form")

var wireTime = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

// wireCases are the commands whose wire form is frozen; every operation, with and without the optional fields.
func wireCases() map[string]Command {
	const (
		u1 = "6f1f7a52-3c1d-4f0e-9a64-0c5b1d8e2a10"
		u2 = "b2c7d4e9-58aa-4b7e-8d0f-77a1e3c9f5d2"
	)
	return map[string]Command{
		"put_create_minimal": {Version: CommandVersion, ID: "c-1", Time: wireTime, Op: OpPut, Kind: api.KindNetwork,
			UUID: u1, Name: "lan", Scope: api.ScopeFleet, Spec: json.RawMessage(`{}`), ExpectGeneration: 0},
		"put_create_all_fields": {Version: CommandVersion, ID: "c-2", Time: wireTime, Op: OpPut, Kind: api.KindFirewallPolicy,
			UUID: u1, Name: "web-access", Scope: api.ScopeHost, Project: "shop", Host: "host-a",
			Labels: map[string]string{"tier": "web", "env": "prod"}, ManagedBy: "hub",
			Spec: json.RawMessage(`{"default_in":"deny","rules":[{"action":"allow","ports":["80","443"]}]}`), ExpectGeneration: 0,
			MoveProject: false},
		"put_update_cas": {Version: CommandVersion, ID: "c-3", Time: wireTime.Add(90 * time.Second), Op: OpPut, Kind: api.KindAddressSet,
			UUID: u2, Name: "office", Scope: api.ScopeFleet, Project: "shop", Host: "host-b",
			Spec: json.RawMessage(`{"members":["192.0.2.0/24","2001:db8::/32"]}`), ExpectGeneration: 3},
		"put_any_generation_move_project": {Version: CommandVersion, ID: "c-4", Time: wireTime, Op: OpPut, Kind: api.KindProject,
			UUID: u2, Name: "shop", Scope: api.ScopeHost, Project: "shop2", Spec: json.RawMessage(`{"networks":["lan"]}`),
			ExpectGeneration: AnyGeneration, MoveProject: true},
		"put_by_name_without_uuid_and_scope": {Version: CommandVersion, ID: "c-5", Time: wireTime, Op: OpPut, Kind: api.KindProfile,
			Name: "small", Spec: json.RawMessage(`{"limits":{"memory":"512M"}}`), ExpectGeneration: 2},
		"put_time_with_offset_and_fraction": {Version: CommandVersion, ID: "c-6",
			Time: time.Date(2026, 11, 19, 11, 30, 15, 123456789, time.FixedZone("", 3600)), Op: OpPut, Kind: api.KindImage,
			UUID: u1, Name: "freebsd-15-1", Scope: api.ScopeFleet, Spec: json.RawMessage(`{"release":"15.1-RELEASE"}`), ExpectGeneration: 0},
		"delete_by_uuid": {Version: CommandVersion, ID: "d-1", Time: wireTime, Op: OpDelete, Kind: api.KindNetwork, UUID: u1},
		"delete_by_uuid_cas": {Version: CommandVersion, ID: "d-2", Time: wireTime, Op: OpDelete, Kind: api.KindNetwork, UUID: u1,
			ExpectGeneration: 7},
		"delete_by_name": {Version: CommandVersion, ID: "d-3", Time: wireTime, Op: OpDelete, Kind: api.KindAddressSet,
			Name: "office", Scope: api.ScopeFleet, Project: "shop", ExpectGeneration: AnyGeneration},
	}
}

func goldenPath(name string) string { return filepath.Join("testdata", "command", name+".golden") }

// indented is the wire form of c, one field per line in the order the struct writes them.
func indented(t *testing.T, c Command) string {
	t.Helper()
	b, err := c.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		t.Fatalf("Indent: %v", err)
	}
	out.WriteByte('\n')
	return out.String()
}

func TestFreezeCommandWireForm(t *testing.T) {
	for name, c := range wireCases() {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err != nil {
				t.Fatalf("the frozen case %s is not a valid command: %v", name, err)
			}
			got := indented(t, c)
			path := goldenPath(name)
			if *freezeUpdate {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (go test ./internal/state -run Freeze -freeze-update writes it)", err)
			}
			if string(want) != got {
				t.Errorf("the wire form of the command %q changed (go test ./internal/state -run Freeze -freeze-update accepts it; "+
					"raise CommandVersion when the meaning of an existing field changes):\n--- want\n%s--- got\n%s", name, want, got)
			}
		})
	}
}

// Every golden file belongs to a case: a case that is removed takes its file with it.
func TestFreezeCommandWireFilesHaveCases(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("testdata", "command"))
	if err != nil {
		t.Fatal(err)
	}
	cases := wireCases()
	for _, e := range ents {
		name := strings.TrimSuffix(e.Name(), ".golden")
		if _, ok := cases[name]; !ok || !strings.HasSuffix(e.Name(), ".golden") {
			t.Errorf("testdata/command/%s has no case in wireCases", e.Name())
		}
	}
}

// What is written is what is read: a command that goes through the wire comes back identical (the time as the same
// instant: the offset of the proposer is kept in the text, applying it converts to UTC).
func TestCommandWireRoundTrip(t *testing.T) {
	for name, c := range wireCases() {
		t.Run(name, func(t *testing.T) {
			b, err := c.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			back, err := UnmarshalCommand(b)
			if err != nil {
				t.Fatalf("UnmarshalCommand: %v", err)
			}
			if !back.Time.Equal(c.Time) {
				t.Errorf("time %v came back as %v", c.Time, back.Time)
			}
			b2, err := back.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, b2) {
				t.Errorf("a second round trip differs:\n%s\n%s", b, b2)
			}
		})
	}
}

func decodeErrCode(t *testing.T, doc string) (Command, api.Code, string) {
	t.Helper()
	c, err := UnmarshalCommand([]byte(doc))
	if err == nil {
		return c, "", ""
	}
	var ae *api.Error
	if !errors.As(err, &ae) {
		t.Fatalf("the error of decoding %s is not an api.Error: %T %v", doc, err, err)
	}
	return c, ae.Code, ae.Message
}

// The decoder of a command, as it is today:
//   - a version other than CommandVersion is refused with version_skew, an older and a newer one alike, so a replica
//     never applies a command written by a binary whose commands it does not understand;
//   - an unknown operation is refused as invalid;
//   - a field the struct does not know is IGNORED (encoding/json without DisallowUnknownFields): the command is applied
//     without it. A change that adds a field with a meaning of its own must therefore raise CommandVersion, otherwise an
//     older replica applies the command without the meaning. The test pins this so that a change of the rule (refusing
//     unknown fields) is made on purpose.
func TestCommandDecode(t *testing.T) {
	const base = `"id":"c-1","time":"2026-11-19T10:00:00Z","op":"put","kind":"Network","uuid":"6f1f7a52-3c1d-4f0e-9a64-0c5b1d8e2a10","name":"lan","spec":{},"expect_generation":0`
	good := `{"version":1,` + base + `}`

	t.Run("a good command", func(t *testing.T) {
		if _, code, msg := decodeErrCode(t, good); code != "" {
			t.Fatalf("refused: %s %s", code, msg)
		}
	})
	for _, v := range []string{"0", "2", "99", "-1"} {
		t.Run("version "+v+" is refused", func(t *testing.T) {
			doc := `{"version":` + v + `,` + base + `}`
			_, code, msg := decodeErrCode(t, doc)
			if code != api.CodeVersionSkew || !strings.Contains(msg, "not supported") {
				t.Fatalf("version %s: code %q message %q, want version_skew naming the unsupported version", v, code, msg)
			}
		})
	}
	t.Run("a missing version is refused", func(t *testing.T) {
		_, code, _ := decodeErrCode(t, `{`+base+`}`)
		if code != api.CodeVersionSkew {
			t.Fatalf("code %q, want version_skew", code)
		}
	})
	t.Run("an unknown operation is refused", func(t *testing.T) {
		doc := strings.Replace(good, `"op":"put"`, `"op":"patch"`, 1)
		_, code, msg := decodeErrCode(t, doc)
		if code != api.CodeInvalid || !strings.Contains(msg, "patch") {
			t.Fatalf("code %q message %q", code, msg)
		}
	})
	t.Run("an unknown field is ignored", func(t *testing.T) {
		doc := `{"version":1,"future_field":{"x":1},` + base + `}`
		c, code, msg := decodeErrCode(t, doc)
		if code != "" {
			t.Fatalf("an unknown field refused the command (%s %s): the rule changed, so say so in CommandVersion and in the docs", code, msg)
		}
		b, _ := c.Marshal()
		if strings.Contains(string(b), "future_field") {
			t.Fatalf("the unknown field survived: %s", b)
		}
	})
	t.Run("damaged documents are refused", func(t *testing.T) {
		for name, doc := range map[string]string{
			"not json":         `{"version":1`,
			"empty":            ``,
			"a list":           `[]`,
			"a number as id":   `{"version":1,"id":7}`,
			"no id":            strings.Replace(good, `"id":"c-1",`, ``, 1),
			"no time":          strings.Replace(good, `"time":"2026-11-19T10:00:00Z",`, ``, 1),
			"spec not json":    strings.Replace(good, `"spec":{}`, `"spec":nope`, 1),
			"put without name": strings.Replace(good, `"name":"lan",`, ``, 1),
			"bad uuid":         strings.Replace(good, `6f1f7a52-3c1d-4f0e-9a64-0c5b1d8e2a10`, `x`, 1),
		} {
			if _, code, _ := decodeErrCode(t, doc); code == "" {
				t.Errorf("%s: the command was accepted", name)
			}
		}
	})
	t.Run("delete needs a uuid or a name", func(t *testing.T) {
		doc := `{"version":1,"id":"d-1","time":"2026-11-19T10:00:00Z","op":"delete","kind":"Network","expect_generation":0}`
		if _, code, _ := decodeErrCode(t, doc); code != api.CodeInvalid {
			t.Fatalf("code %q, want invalid", code)
		}
	})
}
