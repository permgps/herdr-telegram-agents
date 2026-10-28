package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// ccTestSessionID is made up; ccTestSecret marks text that must never reach
// a log line.
const (
	ccTestSessionID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	ccTestSecret    = "PRIVATE-CC-OUTPUT"
)

var (
	ccT0 = time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC) // the prompt
	ccT1 = ccT0.Add(2 * time.Minute)                      // the answer
)

// ccLine renders one session line.
func ccLine(rec map[string]any) string {
	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ccHeader is the session header Command Code writes first; its id must
// match the Herdr session value.
func ccHeader() string {
	return ccLine(map[string]any{"type": "session", "version": 3, "id": ccTestSessionID,
		"timestamp": ccT0.Add(-time.Minute).Format(time.RFC3339Nano), "cwd": "/work/project"})
}

// ccAssistant is one assistant API response: content blocks plus the model
// and usage Command Code records at the top level of the entry.
func ccAssistant(id string, at time.Time, blocks []any, model string, out int) string {
	return ccLine(map[string]any{"type": "message", "id": id, "parentId": nil,
		"timestamp": at.Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "assistant", "content": blocks},
		"model":     model,
		"usage":     map[string]any{"inputTokens": 10, "outputTokens": out, "cacheReadTokens": 0, "cacheWriteTokens": 0}})
}

// ccUserText is an operator prompt.
func ccUserText(id string, at time.Time, text string) string {
	return ccLine(map[string]any{"type": "message", "id": id, "parentId": nil,
		"timestamp": at.Format(time.RFC3339Nano),
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
		}}})
}

// ccToolResult is the user record carrying a tool's output.
func ccToolResult(id string, at time.Time) string {
	return ccLine(map[string]any{"type": "message", "id": id, "parentId": nil,
		"timestamp": at.Format(time.RFC3339Nano),
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": ccTestSecret},
		}}})
}

// ccToolStep is an assistant response that ends in a tool call: commentary
// at most, never the turn's answer.
func ccToolStep(id string, at time.Time, tool, path string, out int) string {
	return ccAssistant(id, at, []any{
		map[string]any{"type": "thinking", "thinking": "hidden " + ccTestSecret},
		map[string]any{"type": "text", "text": "let me look"},
		map[string]any{"type": "tool_use", "id": "t-" + id, "name": tool, "input": map[string]any{"file_path": path}},
	}, "deepseek-x", out)
}

// ccAnswer is an assistant response that ends in prose: the turn's answer.
func ccAnswer(id string, at time.Time, text string, out int) string {
	return ccAssistant(id, at, []any{
		map[string]any{"type": "thinking", "thinking": "hidden " + ccTestSecret},
		map[string]any{"type": "text", "text": text},
	}, "deepseek-opus-x", out)
}

// ccNonMessage renders one of the other line types Command Code appends
// (model_change, session_info, effort_change, compaction).
func ccNonMessage(typ, id string, at time.Time) string {
	return ccLine(map[string]any{"type": typ, "id": id, "parentId": nil,
		"timestamp": at.Format(time.RFC3339Nano), "name": "quiet"})
}

// ccTurn is a finished turn: prompt, a tool step that edits a file, its
// result, a write, its result, the answer.
func ccTurn(prefix, prompt, answer string) []string {
	return []string{
		ccUserText(prefix+"u", ccT0, prompt),
		ccToolStep(prefix+"a1", ccT0.Add(time.Second), "edit_file", "/work/project/a.go", 7),
		ccToolResult(prefix+"r1", ccT0.Add(2*time.Second)),
		ccToolStep(prefix+"a2", ccT0.Add(3*time.Second), "write_file", "/work/project/b.go", 5),
		ccToolResult(prefix+"r2", ccT0.Add(4*time.Second)),
		ccAnswer(prefix+"a3", ccT1, answer, 100),
	}
}

type ccFixture struct {
	t      *testing.T
	home   string
	dir    string
	path   string
	tuple  domain.SessionTuple
	digest string
	logBuf bytes.Buffer
}

func newCCFixture(t *testing.T) *ccFixture {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".commandcode", "projects", "work-project")
	tuple := domain.SessionTuple{Source: "herdr:command-code", Agent: "command-code", Kind: "id", Value: ccTestSessionID}
	return &ccFixture{t: t, home: home, dir: dir, path: filepath.Join(dir, ccTestSessionID+".jsonl"), tuple: tuple, digest: tuple.Digest()}
}

// write puts the header and lines into the session file.
func (f *ccFixture) write(lines ...string) {
	f.t.Helper()
	f.writeRaw(strings.Join(append([]string{ccHeader()}, lines...), "\n") + "\n")
}

func (f *ccFixture) writeRaw(body string) {
	f.t.Helper()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *ccFixture) reader() *CommandCodeReader {
	session := func(context.Context, string) (domain.SessionTuple, error) { return f.tuple, nil }
	now := func() time.Time { return ccT1.Add(5 * time.Second) }
	home := func() (string, error) { return f.home, nil }
	return newCommandCodeReader(home, session, now, slog.New(slog.NewJSONHandler(&f.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func (f *ccFixture) agent() domain.Agent {
	return domain.Agent{Key: domain.Key{PaneID: "w1:p1", TerminalID: "term-1", SessionDigest: f.digest}, Kind: "command-code", Cwd: "/work/project"}
}

func (f *ccFixture) read() (domain.Reply, error) {
	return f.reader().LastReply(context.Background(), f.agent())
}

// assertLogClean fails when a log line carries the session id, a path or
// session text.
func (f *ccFixture) assertLogClean() {
	f.t.Helper()
	logs := f.logBuf.String()
	for _, secret := range []string{ccTestSessionID, ccTestSecret, f.dir, f.home, "work-project", "the answer"} {
		if strings.Contains(logs, secret) {
			f.t.Fatalf("log leaks %q: %s", secret, logs)
		}
	}
}

func TestCommandCodeLastReplyReturnsAnswer(t *testing.T) {
	f := newCCFixture(t)
	f.write(append(ccTurn("o", "old", "an older answer"), ccTurn("n", "hi", "the answer\n\n- one\n- two")...)...)

	r, err := f.read()
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if r.Text != "the answer\n\n- one\n- two" || r.Source != "command-code session" {
		t.Fatalf("reply = %q from %q", r.Text, r.Source)
	}
	if !r.Meta.Started.Equal(ccT0) || !r.Meta.Ended.Equal(ccT1) || !r.Written.Equal(ccT1) {
		t.Fatalf("meta = %+v, written %v", r.Meta, r.Written)
	}
	if r.Age != 5*time.Second {
		t.Fatalf("age = %v, want 5s", r.Age)
	}
	if r.Meta.Model != "deepseek-opus-x" || r.Meta.OutputTokens != 112 {
		t.Fatalf("model %q, tokens %d; want deepseek-opus-x, 112 (7+5+100)", r.Meta.Model, r.Meta.OutputTokens)
	}
	if got := strings.Join(r.Meta.Files, ","); got != "/work/project/a.go,/work/project/b.go" {
		t.Fatalf("files = %q, want a.go then b.go once each", got)
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"command-code reply found"`) {
		t.Fatalf("no found line: %s", f.logBuf.String())
	}
	f.assertLogClean()
}

// A step that ends in a tool call is never the answer, and thinking is
// never posted.
func TestCommandCodeLastReplyNeverPostsCommentary(t *testing.T) {
	f := newCCFixture(t)
	f.write(
		ccUserText("u", ccT0, "hi"),
		ccToolStep("a1", ccT0.Add(time.Second), "edit_file", "/work/project/a.go", 7),
		ccToolResult("r1", ccT0.Add(2*time.Second)),
		ccAnswer("a2", ccT1, "final", 9),
	)
	r, err := f.read()
	if err != nil || r.Text != "final" {
		t.Fatalf("reply = %q, %v; want only the final answer", r.Text, err)
	}
	if strings.Contains(r.Text, "hidden") || strings.Contains(r.Text, "let me look") {
		t.Fatalf("commentary posted: %q", r.Text)
	}
}

// Every shape of a running turn answers ErrReplyPending, so the caller
// waits instead of posting a fragment.
func TestCommandCodeLastReplyPendingWhileRunning(t *testing.T) {
	steps := map[string][]string{
		"newest is a tool call":   {ccUserText("u", ccT0, "hi"), ccToolStep("a1", ccT0.Add(time.Second), "shell_command", "", 7)},
		"newest is a tool result": {ccUserText("u", ccT0, "hi"), ccToolStep("a1", ccT0.Add(time.Second), "read_file", "", 7), ccToolResult("r1", ccT0.Add(2*time.Second))},
		"newest is a prompt":      {ccUserText("u", ccT0, "hi")},
		"newest is thinking only": {ccUserText("u", ccT0, "hi"), ccAssistant("a1", ccT0.Add(time.Second), []any{map[string]any{"type": "thinking", "thinking": ccTestSecret}}, "deepseek-x", 3)},
		"text then a tool call":   {ccUserText("u", ccT0, "hi"), ccToolStep("a1", ccT0.Add(time.Second), "edit_file", "/work/project/a.go", 7)},
		"answer, then new prompt": {ccUserText("u", ccT0, "hi"), ccAnswer("a1", ccT1, "first", 4), ccUserText("u2", ccT1.Add(time.Second), "again")},
	}
	for name, lines := range steps {
		t.Run(name, func(t *testing.T) {
			f := newCCFixture(t)
			f.write(lines...)
			if _, err := f.read(); !errors.Is(err, domain.ErrReplyPending) {
				t.Fatalf("err = %v, want ErrReplyPending", err)
			}
		})
	}
}

// The reader walks back over non-message lines (model_change, session_info,
// effort_change, compaction) to the answer.
func TestCommandCodeSkipsNonMessageEntries(t *testing.T) {
	f := newCCFixture(t)
	f.write(append(ccTurn("n", "hi", "final"),
		ccNonMessage("session_info", "s1", ccT1.Add(time.Second)),
		ccNonMessage("model_change", "s2", ccT1.Add(2*time.Second)),
		ccNonMessage("effort_change", "s3", ccT1.Add(3*time.Second)),
		ccNonMessage("compaction", "s4", ccT1.Add(4*time.Second)),
	)...)
	r, err := f.read()
	if err != nil || r.Text != "final" {
		t.Fatalf("reply = %q, %v; trailing entries must not hide the answer", r.Text, err)
	}
}

func TestCommandCodeRejectsForeignFiles(t *testing.T) {
	t.Run("another session in the file", func(t *testing.T) {
		f := newCCFixture(t)
		f.write(ccAnswer("a1", ccT1, "the answer", 4))
		f.writeRaw(strings.ReplaceAll(f.readRaw(), ccTestSessionID, "ffffffff-0000-4000-8000-000000000000"))
		if _, err := f.read(); !errors.Is(err, domain.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply for a foreign header", err)
		}
	})
	t.Run("not a session file", func(t *testing.T) {
		f := newCCFixture(t)
		f.writeRaw(`{"type":"message","message":{"role":"assistant"}}` + "\n")
		if _, err := f.read(); !errors.Is(err, domain.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply for a missing header", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		f := newCCFixture(t)
		if _, err := f.read(); !errors.Is(err, domain.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply for a missing session file", err)
		}
	})
}

// A pane that now runs another session never gets this topic's answer.
func TestCommandCodeRejectsOtherSessions(t *testing.T) {
	f := newCCFixture(t)
	f.write(ccAnswer("a1", ccT1, "the answer", 4))
	agent := f.agent()
	agent.SessionDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := f.reader().LastReply(context.Background(), agent); !errors.Is(err, domain.ErrNoReply) {
		t.Fatalf("err = %v, want ErrNoReply for another session", err)
	}
}

func TestCommandCodeRejectsOtherKinds(t *testing.T) {
	f := newCCFixture(t)
	f.write(ccAnswer("a1", ccT1, "the answer", 4))
	agent := f.agent()
	agent.Kind = "claude"
	if _, err := f.reader().LastReply(context.Background(), agent); !errors.Is(err, domain.ErrUnsupportedAgent) {
		t.Fatalf("err = %v, want ErrUnsupportedAgent", err)
	}
	agent = f.agent()
	if _, err := f.reader().LastReply(context.Background(), agent); err != nil {
		t.Fatalf("LastReply: %v", err)
	}
}

// The session file is found by its exact id wherever Command Code put it,
// with no guess from the working directory: how Command Code names that
// directory (lower case, dashes at case boundaries) is its own business, so
// a cwd-less agent reads the same file.
func TestCommandCodeFindsSessionByID(t *testing.T) {
	f := newCCFixture(t)
	f.dir = filepath.Join(f.home, ".commandcode", "projects", "some-other-project")
	f.path = filepath.Join(f.dir, ccTestSessionID+".jsonl")
	f.write(ccAnswer("a1", ccT1, "the answer", 4))

	agent := f.agent()
	agent.Cwd = ""
	r, err := f.reader().LastReply(context.Background(), agent)
	if err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v; want the answer from its own project directory", r.Text, err)
	}
}

// readRaw returns what the session file holds, for the foreign-file test.
func (f *ccFixture) readRaw() string {
	f.t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}
