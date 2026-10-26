package fake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The fake serves the session protocol of the web console: POST opens a
// session, GET .../output streams server-sent events, POST .../input and
// .../resize and DELETE do the rest. A session behaves like "cat" on a terminal:
// input is echoed, Ctrl-D ends it with status 0, DELETE ends it with 137. A
// session opened with argv "echo ARGS" prints and ends at once.

// ConsoleCall is one console the fake opened.
type ConsoleCall struct {
	Jail       string
	Rows, Cols uint16
	// Resizes lists the sizes sent after opening.
	Resizes [][2]uint16
}

type fakeConsole struct {
	jail    string
	project string
	mu      sync.Mutex
	cond    *sync.Cond
	events  []fakeConsoleEvent
	done    bool
	call    int
}

type fakeConsoleEvent struct {
	data []byte
	exit *int
}

func (c *fakeConsole) push(data []byte, exit *int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return
	}
	c.events = append(c.events, fakeConsoleEvent{data: data, exit: exit})
	if exit != nil {
		c.done = true
	}
	c.cond.Broadcast()
}

// ConsoleCalls returns the consoles opened so far.
func (s *Server) ConsoleCalls() []ConsoleCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ConsoleCall(nil), s.consoleCalls...)
}

func (s *Server) consoleOpen(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.ExecRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name := r.PathValue("name")
	s.mu.Lock()
	_, j := s.jails.find(r, name)
	var perr *api.Error
	switch {
	case j == nil:
		perr = notFound("jail", name)
	case j.Status.Power != api.PowerRunning:
		perr = api.Errorf(api.CodeBusy, "jail %q is not running", name)
	}
	if perr != nil {
		s.mu.Unlock()
		writeErr(w, perr)
		return
	}
	s.nextConsole++
	id := "c" + strconv.Itoa(s.nextConsole)
	c := &fakeConsole{jail: name, project: s.info(r).project, call: len(s.consoleCalls)}
	c.cond = sync.NewCond(&c.mu)
	if s.consoles == nil {
		s.consoles = map[string]*fakeConsole{}
	}
	s.consoles[id] = c
	s.consoleCalls = append(s.consoleCalls, ConsoleCall{Jail: name, Rows: req.Rows, Cols: req.Cols})
	s.mu.Unlock()
	if len(req.Argv) > 0 && req.Argv[0] == "echo" {
		zero := 0
		c.push([]byte(strings.Join(req.Argv[1:], " ")+"\n"), nil)
		c.push(nil, &zero)
	}
	writeSync(w, 200, map[string]string{"id": id})
}

func (s *Server) consoleFor(w http.ResponseWriter, r *http.Request) *fakeConsole {
	s.mu.Lock()
	c := s.consoles[r.PathValue("id")]
	s.mu.Unlock()
	if c == nil || c.jail != r.PathValue("name") {
		writeErr(w, api.Errorf(api.CodeNotFound, "console session %q does not exist", r.PathValue("id")))
		return nil
	}
	return c
}

func (s *Server) consoleInput(w http.ResponseWriter, r *http.Request) {
	c := s.consoleFor(w, r)
	if c == nil {
		return
	}
	in, e := decode[struct {
		Data string `json:"data"`
	}](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	c.mu.Lock()
	ended := c.done
	c.mu.Unlock()
	if ended {
		writeErr(w, api.Errorf(api.CodeBusy, "the console session has ended"))
		return
	}
	if before, _, found := strings.Cut(in.Data, "\x04"); found {
		if before != "" {
			c.push([]byte(before), nil)
		}
		zero := 0
		c.push(nil, &zero)
	} else {
		c.push([]byte(in.Data), nil)
	}
	writeSync(w, 200, struct{}{})
}

func (s *Server) consoleResize(w http.ResponseWriter, r *http.Request) {
	c := s.consoleFor(w, r)
	if c == nil {
		return
	}
	in, e := decode[struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	if in.Cols < 1 || in.Rows < 1 || in.Cols > 1000 || in.Rows > 1000 {
		writeErr(w, api.Errorf(api.CodeInvalid, "the terminal size must be between 1 and 1000"))
		return
	}
	s.mu.Lock()
	s.consoleCalls[c.call].Resizes = append(s.consoleCalls[c.call].Resizes, [2]uint16{in.Rows, in.Cols})
	s.mu.Unlock()
	writeSync(w, 200, struct{}{})
}

func (s *Server) consoleClose(w http.ResponseWriter, r *http.Request) {
	c := s.consoleFor(w, r)
	if c == nil {
		return
	}
	killed := 137
	c.push(nil, &killed)
	writeSync(w, 200, struct{}{})
}

func (s *Server) consoleOutput(w http.ResponseWriter, r *http.Request) {
	c := s.consoleFor(w, r)
	if c == nil {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, api.Errorf(api.CodeInternal, "streaming unsupported"))
		return
	}
	after := 0
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil || n < 0 {
			writeErr(w, api.Errorf(api.CodeInvalid, "invalid Last-Event-ID %q", h))
			return
		}
		after = n
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-r.Context().Done():
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		case <-stop:
		}
	}()
	for {
		c.mu.Lock()
		for len(c.events) <= after && r.Context().Err() == nil {
			c.cond.Wait()
		}
		batch := append([]fakeConsoleEvent(nil), c.events[min(after, len(c.events)):]...)
		c.mu.Unlock()
		if r.Context().Err() != nil {
			return
		}
		for _, ev := range batch {
			after++
			var meta []byte
			if ev.exit != nil {
				meta, _ = json.Marshal(map[string]int{"exit": *ev.exit})
			} else {
				meta, _ = json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(ev.data)})
			}
			body, _ := json.Marshal(api.Event{Type: "console", Timestamp: time.Now().UTC(), Metadata: meta})
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", after, body)
			fl.Flush()
			if ev.exit != nil {
				return
			}
		}
	}
}
