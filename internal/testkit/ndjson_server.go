// Package testkit holds in-memory fakes and test servers shared by adapter
// and application tests. It imports only the domain package from this
// module and never talks to the network beyond the loopback fakes it owns.
package testkit

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// APIError is the error half of a Herdr response line.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HandlerFunc answers one request. Exactly one of result or apiErr should
// be non-nil; a nil result with a nil error answers `{}`.
type HandlerFunc func(id string, params json.RawMessage) (result any, apiErr *APIError)

// Request is one decoded request line, kept for assertions.
type Request struct {
	Conn   int // 1-based index of the connection that sent it
	ID     string
	Method string
	Params json.RawMessage
}

type requestLine struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type responseLine struct {
	ID     string    `json:"id"`
	Result any       `json:"result,omitempty"`
	Error  *APIError `json:"error,omitempty"`
}

type envelope struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type connState struct {
	index      int
	conn       net.Conn
	writeMu    sync.Mutex
	subscribed bool
}

// NDJSONServer is a fake Herdr socket server speaking the newline-delimited
// JSON protocol on a temporary Unix socket.
//
// It mirrors the verified Herdr behaviour that matters to the adapter:
// `events.subscribe` answers `subscription_started` and turns the
// connection into a write-once stream, and any further request on that
// connection makes the server close it.
type NDJSONServer struct {
	tb  testing.TB
	log *slog.Logger
	dir string
	ln  net.Listener

	mu       sync.Mutex
	handlers map[string]HandlerFunc
	conns    map[*connState]struct{}
	requests []Request
	connSeq  int
	closed   bool

	wg sync.WaitGroup
}

// NewNDJSONServer starts a server on a fresh socket path and registers its
// shutdown with tb.Cleanup. It skips the test on Windows, where Herdr uses
// a named pipe that this fake does not emulate. log may be nil.
func NewNDJSONServer(tb testing.TB, log *slog.Logger) *NDJSONServer {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("NDJSONServer uses a unix socket; not available on windows")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Not tb.TempDir(): its path can exceed the 104-byte sun_path limit on
	// macOS once the test name is folded in.
	dir, err := os.MkdirTemp("", "hs")
	if err != nil {
		tb.Fatalf("testkit: mkdtemp: %v", err)
	}
	path := filepath.Join(dir, "herdr.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		tb.Fatalf("testkit: listen %s: %v", path, err)
	}
	s := &NDJSONServer{
		tb:       tb,
		log:      log,
		dir:      dir,
		ln:       ln,
		handlers: map[string]HandlerFunc{},
		conns:    map[*connState]struct{}{},
	}
	s.wg.Add(1)
	go s.acceptLoop()
	tb.Cleanup(s.Close)
	return s
}

// Path returns the socket path clients should dial.
func (s *NDJSONServer) Path() string {
	return s.ln.Addr().String()
}

// Handle registers the handler for a method, replacing any previous one.
func (s *NDJSONServer) Handle(method string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = fn
}

// Requests returns a copy of every request received so far, in order.
func (s *NDJSONServer) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// WaitRequests blocks until at least n requests for method have arrived or
// the timeout passes, returning the matching requests either way.
func (s *NDJSONServer) WaitRequests(method string, n int, timeout time.Duration) []Request {
	deadline := time.Now().Add(timeout)
	for {
		var got []Request
		for _, r := range s.Requests() {
			if r.Method == method {
				got = append(got, r)
			}
		}
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ConnCount returns the number of currently open connections.
func (s *NDJSONServer) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// SubscriptionCount returns how many open connections are subscriptions.
func (s *NDJSONServer) SubscriptionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		if c.subscribed {
			n++
		}
	}
	return n
}

// WaitConns blocks until the open connection count equals want or the
// timeout passes, and reports whether it got there.
func (s *NDJSONServer) WaitConns(want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for s.ConnCount() != want {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// Push writes an event envelope to every subscription connection and
// returns how many connections received it.
func (s *NDJSONServer) Push(event string, data any) int {
	line, err := json.Marshal(envelope{Event: event, Data: data})
	if err != nil {
		s.tb.Fatalf("testkit: marshal event %s: %v", event, err)
	}
	s.mu.Lock()
	var targets []*connState
	for c := range s.conns {
		if c.subscribed {
			targets = append(targets, c)
		}
	}
	s.mu.Unlock()
	n := 0
	for _, c := range targets {
		if s.writeLine(c, line) == nil {
			n++
		}
	}
	s.log.Debug("testkit push", slog.String("event", event), slog.Int("conns", n))
	return n
}

// CloseAll drops every open connection but keeps listening, which is how a
// Herdr restart looks to a client.
func (s *NDJSONServer) CloseAll() {
	s.mu.Lock()
	conns := make([]*connState, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.Close()
	}
}

// Close stops the listener, drops all connections and removes the socket.
// It is safe to call more than once.
func (s *NDJSONServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.CloseAll()
	s.wg.Wait()
	_ = os.RemoveAll(s.dir)
}

func (s *NDJSONServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connSeq++
		c := &connState{index: s.connSeq, conn: conn}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(c)
	}
}

func (s *NDJSONServer) serve(c *connState) {
	defer s.wg.Done()
	defer func() {
		_ = c.conn.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var req requestLine
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			s.log.Debug("testkit bad line", slog.Int("conn", c.index), slog.String("err", err.Error()))
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, Request{Conn: c.index, ID: req.ID, Method: req.Method, Params: req.Params})
		subscribed := c.subscribed
		fn := s.handlers[req.Method]
		s.mu.Unlock()
		s.log.Debug("testkit request", slog.Int("conn", c.index), slog.String("method", req.Method), slog.String("id", req.ID))

		if subscribed {
			// Herdr closes a subscription connection that speaks again.
			return
		}
		if req.Method == "events.subscribe" {
			if err := s.reply(c, responseLine{ID: req.ID, Result: map[string]string{"type": "subscription_started"}}); err != nil {
				return
			}
			s.mu.Lock()
			c.subscribed = true
			s.mu.Unlock()
			continue
		}
		var resp responseLine
		switch {
		case fn == nil:
			resp = responseLine{ID: req.ID, Error: &APIError{Code: "unknown_method", Message: "unknown method " + req.Method}}
		default:
			result, apiErr := fn(req.ID, req.Params)
			if apiErr != nil {
				resp = responseLine{ID: req.ID, Error: apiErr}
			} else {
				if result == nil {
					result = map[string]any{}
				}
				resp = responseLine{ID: req.ID, Result: result}
			}
		}
		if err := s.reply(c, resp); err != nil {
			return
		}
	}
}

func (s *NDJSONServer) reply(c *connState, resp responseLine) error {
	line, err := json.Marshal(resp)
	if err != nil {
		s.tb.Fatalf("testkit: marshal response: %v", err)
	}
	return s.writeLine(c, line)
}

func (s *NDJSONServer) writeLine(c *connState, line []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(append(line, '\n'))
	return err
}
