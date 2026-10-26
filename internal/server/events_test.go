package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestEventsStreamAndFilter(t *testing.T) {
	e := seeded(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got := make(chan api.Event, 64)
	go func() {
		for ev, err := range e.Admin.Events(ctx, sdk.Types(api.EventLifecycle)) {
			if err != nil {
				return
			}
			got <- ev
		}
	}()
	time.Sleep(200 * time.Millisecond) // subscribed
	mustJail(t, e, "web")
	want := map[string]bool{"jail-created": false}
	deadline := time.After(10 * time.Second)
	for !want["jail-created"] {
		select {
		case ev := <-got:
			if ev.Type != api.EventLifecycle {
				t.Fatalf("filter let %s through", ev.Type)
			}
			var le api.LifecycleEvent
			_ = json.Unmarshal(ev.Metadata, &le)
			if le.Action == "jail-created" && le.Source == "/1.0/jails/web" {
				want["jail-created"] = true
				if le.Requestor == "" || ev.Host != e.D.Service().HostUUID() {
					t.Fatalf("%+v %+v", le, ev)
				}
			}
		case <-deadline:
			t.Fatal("no jail-created event")
		}
	}
}

// A client that reconnects with Last-Event-ID gets what it missed, in order,
// without duplicates, then live events.
func TestEventsReplayFromLastEventID(t *testing.T) {
	e := seeded(t)
	tok := e.Token(api.PermAdmin)
	read := func(last string, n int) ([]int64, []string) {
		timeout := 10 * time.Second
		if n >= 100 {
			timeout = 1500 * time.Millisecond // drain what is retained, then stop
		}
		req, _ := http.NewRequest("GET", e.HTTP.URL+"/1.0/events?type=lifecycle", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if last != "" {
			req.Header.Set("Last-Event-ID", last)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("%s", ct)
		}
		var ids []int64
		var actions []string
		sc := bufio.NewScanner(resp.Body)
		sawRetry := false
		for sc.Scan() && len(ids) < n {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "retry:"):
				sawRetry = true
			case strings.HasPrefix(l, "id: "):
				id, _ := strconv.ParseInt(l[4:], 10, 64)
				ids = append(ids, id)
			case strings.HasPrefix(l, "data: "):
				var ev api.Event
				_ = json.Unmarshal([]byte(l[6:]), &ev)
				var le api.LifecycleEvent
				_ = json.Unmarshal(ev.Metadata, &le)
				actions = append(actions, le.Action)
			}
		}
		if !sawRetry {
			t.Error("no retry field")
		}
		return ids, actions
	}
	mustJail(t, e, "a")
	first, _ := read("0", 3) // everything retained: image and network and jail events
	if len(first) < 3 {
		t.Fatalf("%v", first)
	}
	mustJail(t, e, "b")
	ids, actions := read(strconv.FormatInt(first[0], 10), 100)
	_ = actions
	if len(ids) == 0 {
		t.Fatal("nothing replayed")
	}
	for i, id := range ids {
		if id <= first[0] {
			t.Fatalf("replayed an old event: %v", ids)
		}
		if i > 0 && id != ids[i-1]+1 {
			// lifecycle-only filtering may skip operation ids but must stay increasing
			if id <= ids[i-1] {
				t.Fatalf("not increasing: %v", ids)
			}
		}
	}
	if !contains(actions, "jail-created") {
		t.Fatalf("%v", actions)
	}
	// Starting "now" shows nothing old.
	done := make(chan []int64, 1)
	go func() { i, _ := read("", 1); done <- i }()
	time.Sleep(300 * time.Millisecond)
	mustJail(t, e, "c")
	select {
	case i := <-done:
		if len(i) != 1 || i[0] <= ids[len(ids)-1] {
			t.Fatalf("%v after %v", i, ids)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no live event")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestEventsBadRequests(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	tok := e.Token(api.PermReadonly)
	if code, _ := e.Raw("GET", "/1.0/events?type=bogus", tok, ""); code != 400 {
		t.Fatalf("%d", code)
	}
	if code, _ := e.RawHdr("GET", "/1.0/events", tok, "", map[string]string{"Last-Event-ID": "abc"}); code != 400 {
		t.Fatalf("%d", code)
	}
	// The UI passes the token in the query: accepted on this route only.
	req, _ := http.NewRequest("GET", e.HTTP.URL+"/1.0/events?access_token="+tok, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp)
	}
	resp.Body.Close()
	if code, _ := e.Raw("GET", "/1.0/operations?access_token="+tok, "", ""); code != 401 {
		t.Fatalf("a token in the query authenticated another route: %d", code)
	}
}
