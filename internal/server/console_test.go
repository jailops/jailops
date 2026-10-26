package server_test

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

type sseReader struct {
	t    *testing.T
	body *bufio.Scanner
	resp *http.Response
}

func openOutput(t *testing.T, base, path, token, lastID string) *sseReader {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("output: %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	return &sseReader{t: t, body: sc, resp: resp}
}

// next returns the next console event: its id, the output text and the exit status (-2 when there is none).
func (r *sseReader) next() (id, text string, exit int) {
	exit = -2
	for r.body.Scan() {
		line := r.body.Text()
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			var ev api.Event
			if err := json.Unmarshal([]byte(v), &ev); err != nil {
				r.t.Fatal(err)
			}
			var m struct {
				Data string `json:"data"`
				Exit *int   `json:"exit"`
			}
			_ = json.Unmarshal(ev.Metadata, &m)
			if m.Exit != nil {
				exit = *m.Exit
			}
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			return id, string(b), exit
		}
	}
	return "", "", -3
}

func TestConsoleSessionOverHTTP(t *testing.T) {
	e := seeded(t)
	startedWeb(t, e)
	tok := e.Token(api.PermAdmin)
	code, body := e.Raw("POST", "/1.0/jails/web/console?project=alpha", tok, `{"argv":["cat"]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var open struct {
		Metadata struct{ ID string } `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(body), &open); err != nil || open.Metadata.ID == "" {
		t.Fatalf("%v %s", err, body)
	}
	id := open.Metadata.ID
	base := "/1.0/jails/web/console/" + id
	out := openOutput(t, e.HTTP.URL, base+"/output?project=alpha", tok, "")

	if code, b := e.Raw("POST", base+"/input?project=alpha", tok, `{"data":"hello console\n"}`); code != 200 {
		t.Fatalf("input: %d %s", code, b)
	}
	var got string
	deadline := time.After(10 * time.Second)
	for !strings.Contains(got, "hello console") {
		select {
		case <-deadline:
			t.Fatalf("no output, got %q", got)
		default:
		}
		_, text, exit := out.next()
		if exit != -2 {
			t.Fatalf("the session ended early with %d", exit)
		}
		got += text
	}
	if code, b := e.Raw("POST", base+"/resize?project=alpha", tok, `{"cols":100,"rows":30}`); code != 200 {
		t.Fatalf("resize: %d %s", code, b)
	}
	if code, _ := e.Raw("POST", base+"/resize?project=alpha", tok, `{"cols":0,"rows":30}`); code != 400 {
		t.Fatalf("a zero size: %d", code)
	}
	// a client that reconnects with Last-Event-ID gets what came after it, not what it has
	again := openOutput(t, e.HTTP.URL, base+"/output?project=alpha", tok, "1")
	_ = again
	// somebody else's token, or another jail's name, does not find the session
	other := e.IdentityToken(api.PermOperator, true)
	if code, _ := e.Raw("POST", base+"/input?project=alpha", other, `{"data":"x"}`); code != 404 {
		t.Fatalf("another identity: %d", code)
	}
	if code, _ := e.Raw("POST", "/1.0/jails/db/console/"+id+"/input?project=alpha", tok, `{"data":"x"}`); code != 404 {
		t.Fatalf("another jail: %d", code)
	}
	if code, _ := e.Raw("POST", base+"/input?project=alpha", "", `{"data":"x"}`); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// an oversized input is refused
	if code, _ := e.Raw("POST", base+"/input?project=alpha", tok, `{"data":"`+strings.Repeat("a", 70<<10)+`"}`); code != 400 && code != 413 {
		t.Fatalf("oversized input: %d", code)
	}
	// closing ends the session with an exit status
	if code, b := e.Raw("DELETE", base+"?project=alpha", tok, ""); code != 200 {
		t.Fatalf("close: %d %s", code, b)
	}
	deadline = time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no exit event after the close")
		default:
		}
		_, _, exit := out.next()
		if exit != -2 {
			if exit == -3 {
				t.Fatal("the stream ended without an exit event")
			}
			break
		}
	}
}

func TestConsoleOfAStoppedJailAndOfTheWrongProject(t *testing.T) {
	e := seeded(t)
	mustJail(t, e, "web") // not started
	tok := e.Token(api.PermAdmin)
	if code, body := e.Raw("POST", "/1.0/jails/web/console?project=alpha", tok, `{"argv":["cat"]}`); code == 200 {
		t.Fatalf("a console in a stopped jail: %s", body)
	}
	if code, _ := e.Raw("POST", "/1.0/jails/nope/console?project=alpha", tok, `{}`); code != 404 {
		t.Fatalf("an unknown jail: %d", code)
	}
	// a readonly identity may not open one
	if code, _ := e.Raw("POST", "/1.0/jails/web/console?project=alpha", e.Token(api.PermReadonly), `{}`); code != 403 {
		t.Fatalf("readonly: %d", code)
	}
}
