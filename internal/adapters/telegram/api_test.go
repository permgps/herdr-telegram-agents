package telegram_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
)

// testToken is the token every test bot uses; its secret part must never
// show up in log output.
const (
	testToken  = "1:TESTSECRET"
	testSecret = "TESTSECRET"
)

type call struct {
	method string
	form   url.Values
	// files holds the multipart file parts by field name, if any.
	files map[string]upload
}

// upload is one multipart file part a fake API received.
type upload struct {
	name string
	data []byte
}

// apiReply describes one canned Bot API response. code 0 is success.
type apiReply struct {
	code   int
	desc   string
	result any
	params map[string]any // retry_after, migrate_to_chat_id
}

func okReply(result any) apiReply { return apiReply{result: result} }

func errReply(code int, desc string) apiReply { return apiReply{code: code, desc: desc} }

func tooManyReply(retryAfter int) apiReply {
	return apiReply{code: 429, desc: "Too Many Requests: retry after " + itoa(retryAfter), params: map[string]any{"retry_after": retryAfter}}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// fakeAPI is an httptest server speaking the Bot API envelope. Every call is
// recorded with its parsed form; replies are configured per method and
// default to {"ok":true,"result":true}.
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []call
	reply  map[string]func(form url.Values) apiReply
	server *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, reply: map[string]func(url.Values) apiReply{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form for %s: %v", method, err)
			}
		}
		var files map[string]upload
		if r.MultipartForm != nil {
			for field, headers := range r.MultipartForm.File {
				if len(headers) == 0 {
					continue
				}
				fh, err := headers[0].Open()
				if err != nil {
					t.Errorf("open multipart file %s: %v", field, err)
					continue
				}
				data, _ := io.ReadAll(fh)
				_ = fh.Close()
				if files == nil {
					files = map[string]upload{}
				}
				files[field] = upload{name: headers[0].Filename, data: data}
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, call{method: method, form: r.Form, files: files})
		fn := f.reply[method]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		rep := okReply(true)
		if fn != nil {
			rep = fn(r.Form)
		}
		if rep.code != 0 {
			body := map[string]any{"ok": false, "error_code": rep.code, "description": rep.desc}
			if rep.params != nil {
				body["parameters"] = rep.params
			}
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": rep.result})
	}))
	t.Cleanup(f.server.Close)
	return f
}

// on installs the reply for a method.
func (f *fakeAPI) on(method string, fn func(form url.Values) apiReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply[method] = fn
}

// once replies with first on the first call and with rest afterwards.
func (f *fakeAPI) once(method string, first, rest apiReply) {
	n := 0
	f.on(method, func(url.Values) apiReply {
		n++
		if n == 1 {
			return first
		}
		return rest
	})
}

// callsOf returns the recorded calls for a method, in order.
func (f *fakeAPI) callsOf(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// methods returns every recorded method name in order.
func (f *fakeAPI) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

// bot builds a client pointed at the fake server through telegram.NewBot.
func (f *fakeAPI) bot(t *testing.T, log *slog.Logger, fatal context.CancelFunc) *bot.Bot {
	t.Helper()
	b, err := telegram.NewBot(testToken, log, fatal, bot.WithServerURL(f.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// logBuffer is a goroutine-safe slog sink so tests can assert on output.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// contains polls the buffer for a substring until the deadline.
func (l *logBuffer) contains(sub string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(l.String()), []byte(sub)) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func newTestLog(t *testing.T) (*slog.Logger, *logBuffer) {
	t.Helper()
	buf := &logBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("log output:\n%s", buf.String())
		}
	})
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// assertNoSecret fails the test if the token secret leaked into the log.
func assertNoSecret(t *testing.T, buf *logBuffer) {
	t.Helper()
	if bytes.Contains([]byte(buf.String()), []byte(testSecret)) {
		t.Errorf("bot token leaked into log output:\n%s", buf.String())
	}
}

// ctxT returns a context that ends with the test.
func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
