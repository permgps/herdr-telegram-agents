package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// The session ids and the pid are made up; museTestSecret marks text that
// must never reach a log line.
const (
	museTestID     = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	museTestOther  = "0199a1b2-c3d4-7e5f-8a9b-ffffffffffff"
	museTestPID    = 48213
	museTestLabel  = "api"
	museTestSecret = "PRIVATE-MUSE-OUTPUT"
	museTestRun    = "run-0001"
)

var (
	museT0 = time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC) // run started
	museT1 = museT0.Add(75 * time.Second)                  // final message
)

// museLine renders one durable log record in the upstream envelope.
func museLine(at time.Time, run string, event map[string]any) string {
	rec := map[string]any{
		"schema_version": 1, "id": "evt", "sequence": 1, "recorded_at": at.UnixMicro(),
		"record_type": "event", "durability": "durable",
		"stream":       map[string]any{"kind": "session", "id": museTestID},
		"payload_type": "runtime.session", "payload_schema_version": 1,
		"payload": map[string]any{"kind": "run", "run_id": run, "event": event},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func museStarted(at time.Time, run string) string {
	return museLine(at, run, map[string]any{"kind": "started", "prompt": "do it " + museTestSecret})
}

func museDelta(at time.Time, run string) string {
	return museLine(at, run, map[string]any{"kind": "text_delta", "message_id": "m", "text": "partial"})
}

// museCommitted is a whole message; phase "" leaves the field out.
func museCommitted(at time.Time, run, phase, text string) string {
	event := map[string]any{"kind": "assistant_message_committed", "message_id": "m", "text": text}
	if phase != "" {
		event["phase"] = phase
	}
	return museLine(at, run, event)
}

func museTerminal(at time.Time, run, terminal string) string {
	return museLine(at, run, map[string]any{"kind": "terminal", "terminal": terminal, "reason": nil})
}

// museTurn is a finished run: start, commentary, the final answer, the end.
func museTurn(run, answer string) []string {
	return []string{
		museStarted(museT0, run),
		museDelta(museT0.Add(time.Second), run),
		museCommitted(museT0.Add(2*time.Second), run, "commentary", "let me look "+museTestSecret),
		museCommitted(museT1, run, "final_answer", answer),
		museTerminal(museT1.Add(time.Second), run, "completed"),
	}
}

type museFixture struct {
	t        *testing.T
	home     string
	data     string
	pids     []int
	procErr  error
	cwd      string
	logBuf   bytes.Buffer
	reader   *MuseReader
	nowValue time.Time
	// started is the injected process start time; startKnown false is an
	// unknown one, as on a platform without the lookup.
	started    time.Time
	startKnown bool
	startedFor []int
}

func newMuseFixture(t *testing.T) *museFixture {
	t.Helper()
	home := t.TempDir()
	f := &museFixture{t: t, home: home, data: filepath.Join(home, museDataDirs[0]), pids: []int{museTestPID},
		cwd: filepath.Join("/work", museTestLabel), nowValue: museT1.Add(5 * time.Second)}
	processes := func(context.Context, string) ([]int, error) { return f.pids, f.procErr }
	homeFn := func() (string, error) { return f.home, nil }
	now := func() time.Time { return f.nowValue }
	started := func(pid int) (time.Time, bool) {
		f.startedFor = append(f.startedFor, pid)
		return f.started, f.startKnown
	}
	f.reader = newMuseReader(processes, started, homeFn, now,
		slog.New(slog.NewJSONHandler(&f.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return f
}

// runtime writes a runtime session file as Muse Code 1.4.3 does.
func (f *museFixture) runtime(id string, pid int, label string) string {
	f.t.Helper()
	body, err := json.Marshal(map[string]any{"schema_version": 1, "session_id": id, "session_name": nil,
		"endpoint_hint": "x", "workspace_label": label, "target_eligibility": "message_capable",
		"process_generation_hint": "pid=" + strconv.Itoa(pid)})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.writeFile(filepath.Join(museRuntimeDir, id+".json"), string(body))
}

// age sets the mtime of a file written below the data directory.
func (f *museFixture) age(path string, mtime time.Time) {
	f.t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		f.t.Fatal(err)
	}
}

// log writes a session's durable log under the given creation date.
func (f *museFixture) log(date, id string, lines ...string) string {
	f.t.Helper()
	return f.logRaw(date, id, strings.Join(lines, "\n")+"\n")
}

func (f *museFixture) logRaw(date, id, body string) string {
	f.t.Helper()
	return f.writeFile(filepath.Join(museSessionsDir, filepath.FromSlash(date), id, museLogName), body)
}

func (f *museFixture) writeFile(rel, body string) string {
	f.t.Helper()
	path := filepath.Join(f.data, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *museFixture) agent() domain.Agent {
	return domain.Agent{Key: domain.Key{PaneID: "w1:p1", TerminalID: "term-1"}, Kind: "muse", Cwd: f.cwd}
}

func (f *museFixture) read() (domain.Reply, error) {
	return f.reader.LastReply(context.Background(), f.agent())
}

// assertLogClean fails when a log line carries a session id, the pid, a
// path or log text.
func (f *museFixture) assertLogClean() {
	f.t.Helper()
	logs := f.logBuf.String()
	for _, secret := range []string{museTestID, museTestOther, strconv.Itoa(museTestPID), museTestSecret, f.home,
		"the answer", museTestRun} {
		if strings.Contains(logs, secret) {
			f.t.Fatalf("log leaks %q: %s", secret, logs)
		}
	}
}

func TestMuseLastReplyReturnsFinalAnswer(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestID, append(museTurn("run-old", "an older answer"), museTurn(museTestRun, "the answer\n\n- one")...)...)

	r, err := f.read()
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if r.Text != "the answer\n\n- one" || r.Source != "muse session log" {
		t.Fatalf("reply = %q from %q", r.Text, r.Source)
	}
	if !r.Meta.Started.Equal(museT0) || !r.Meta.Ended.Equal(museT1) || !r.Written.Equal(museT1) {
		t.Fatalf("meta = %+v written %v", r.Meta, r.Written)
	}
	if r.Age != 5*time.Second {
		t.Fatalf("age = %v", r.Age)
	}
	if !strings.Contains(f.logBuf.String(), `"phase":"final_answer"`) {
		t.Fatalf("phase not logged: %s", f.logBuf.String())
	}
	f.assertLogClean()
}

func TestMuseLastReplyPhaseAbsentIsFinal(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestID,
		museStarted(museT0, museTestRun),
		museCommitted(museT0.Add(time.Second), museTestRun, "commentary", "thinking "+museTestSecret),
		museCommitted(museT1, museTestRun, "", "the answer"),
		museTerminal(museT1, museTestRun, "completed"))

	r, err := f.read()
	if err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	if !strings.Contains(f.logBuf.String(), `"phase":"none"`) {
		t.Fatalf("phase class not logged: %s", f.logBuf.String())
	}
	f.assertLogClean()
}

func TestMuseLastReplyOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		raw     string
		pending bool
	}{
		{name: "commentary only", lines: []string{
			museStarted(museT0, museTestRun),
			museCommitted(museT1, museTestRun, "commentary", "just thinking "+museTestSecret),
			museTerminal(museT1, museTestRun, "completed"),
		}},
		{name: "newer run started", pending: true, lines: append(museTurn("run-old", "old answer"),
			museStarted(museT1.Add(time.Minute), museTestRun))},
		{name: "committed without terminal", pending: true, lines: []string{
			museStarted(museT0, museTestRun),
			museCommitted(museT1, museTestRun, "final_answer", "early "+museTestSecret),
		}},
		{name: "failed", lines: []string{
			museStarted(museT0, museTestRun),
			museCommitted(museT1, museTestRun, "final_answer", "partial "+museTestSecret),
			museTerminal(museT1, museTestRun, "failed"),
		}},
		{name: "cancelled", lines: []string{
			museStarted(museT0, museTestRun),
			museTerminal(museT1, museTestRun, "cancelled"),
		}},
		{name: "unknown terminal", lines: []string{
			museStarted(museT0, museTestRun),
			museTerminal(museT1, museTestRun, "exploded-"+museTestSecret),
		}},
		{name: "mid record", pending: true, raw: strings.Join(museTurn(museTestRun, "the answer"), "\n") + "\n" + `{"recorded_at":1`},
		{name: "no run at all", lines: []string{`{"payload":{"kind":"session"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMuseFixture(t)
			f.runtime(museTestID, museTestPID, museTestLabel)
			if tc.raw != "" {
				f.logRaw("2026/10/06", museTestID, tc.raw)
			} else {
				f.log("2026/10/06", museTestID, tc.lines...)
			}
			_, err := f.read()
			if !errors.Is(err, domain.ErrNoReply) {
				t.Fatalf("err = %v, want ErrNoReply", err)
			}
			if got := errors.Is(err, domain.ErrReplyPending); got != tc.pending {
				t.Fatalf("pending = %v, want %v (%v)", got, tc.pending, err)
			}
			if strings.Contains(err.Error(), museTestSecret) {
				t.Fatalf("error leaks text: %v", err)
			}
			f.assertLogClean()
		})
	}
}

func TestMuseLastReplyIgnoresOtherRuns(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestID,
		museStarted(museT0, museTestRun),
		museCommitted(museT1, museTestRun, "final_answer", "the answer"),
		museCommitted(museT1, "run-other", "final_answer", "not this "+museTestSecret),
		museTerminal(museT1.Add(time.Second), museTestRun, "completed"))

	r, err := f.read()
	if err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
}

func TestMuseLastReplyMatchesThePanePID(t *testing.T) {
	f := newMuseFixture(t)
	// Another Muse pane in a directory with the same basename.
	f.runtime(museTestOther, museTestPID+1, museTestLabel)
	f.log("2026/10/06", museTestOther, museTurn("run-x", "the other pane's answer "+museTestSecret)...)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/05", museTestID, museTurn(museTestRun, "the answer")...)

	r, err := f.read()
	if err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	f.assertLogClean()
}

// TestMuseLastReplyStalePid: a runtime file written before its pid's
// process started belongs to an earlier process that had the same pid, and
// its session's answer is not posted.
func TestMuseLastReplyStalePid(t *testing.T) {
	f := newMuseFixture(t)
	path := f.runtime(museTestOther, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestOther, museTurn(museTestRun, "the answer of a dead process")...)
	f.started, f.startKnown = time.Now(), true
	f.age(path, f.started.Add(-time.Hour))

	_, err := f.read()
	if !errors.Is(err, domain.ErrNoReply) || errors.Is(err, domain.ErrReplyPending) {
		t.Fatalf("err = %v, want ErrNoReply", err)
	}
	if len(f.startedFor) != 1 || f.startedFor[0] != museTestPID {
		t.Fatalf("start time asked for %v", f.startedFor)
	}
	if !strings.Contains(f.logBuf.String(), `"reason":"stale_pid"`) {
		t.Fatalf("stale pid not logged: %s", f.logBuf.String())
	}
	f.assertLogClean()
}

// TestMuseLastReplyStartTimeKeepsCurrentFile: a runtime file written after
// its process started matches, so does one within the clock slack before
// it, and so does any file when the start time is unknown.
func TestMuseLastReplyStartTimeKeepsCurrentFile(t *testing.T) {
	for name, tc := range map[string]struct {
		known  bool
		offset time.Duration
	}{
		"after start":   {known: true, offset: time.Second},
		"within slack":  {known: true, offset: -time.Second},
		"unknown start": {known: false, offset: -time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMuseFixture(t)
			path := f.runtime(museTestID, museTestPID, museTestLabel)
			f.log("2026/10/06", museTestID, museTurn(museTestRun, "the answer")...)
			f.started, f.startKnown = time.Now(), tc.known
			f.age(path, f.started.Add(tc.offset))
			r, err := f.read()
			if err != nil || r.Text != "the answer" {
				t.Fatalf("reply = %q, %v", r.Text, err)
			}
			if strings.Contains(f.logBuf.String(), "stale_pid") {
				t.Fatalf("current file called stale: %s", f.logBuf.String())
			}
			f.assertLogClean()
		})
	}
}

func TestMuseLastReplyNewestLogAfterNew(t *testing.T) {
	f := newMuseFixture(t)
	// /new in the same process: two runtime files with the pane's pid.
	f.runtime(museTestOther, museTestPID, museTestLabel)
	old := f.log("2026/10/06", museTestOther, museTurn("run-x", "the old session's answer")...)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestID, museTurn(museTestRun, "the answer")...)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	r, err := f.read()
	if err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	if !strings.Contains(f.logBuf.String(), `"chosen_by":"newest_log"`) {
		t.Fatalf("choice not logged: %s", f.logBuf.String())
	}
	f.assertLogClean()
}

func TestMuseLastReplyNoReply(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *museFixture)
	}{
		{"wrong label", func(f *museFixture) {
			f.runtime(museTestID, museTestPID, "web")
		}},
		{"no pids", func(f *museFixture) {
			f.runtime(museTestID, museTestPID, museTestLabel)
			f.pids = nil
		}},
		{"process lookup fails", func(f *museFixture) {
			f.runtime(museTestID, museTestPID, museTestLabel)
			f.procErr = errors.New("unknown method " + museTestSecret)
		}},
		{"no cwd", func(f *museFixture) {
			f.runtime(museTestID, museTestPID, museTestLabel)
			f.cwd = ""
		}},
		{"runtime name differs from session id", func(f *museFixture) {
			f.writeFile(filepath.Join(museRuntimeDir, museTestOther+".json"),
				`{"session_id":"`+museTestID+`","workspace_label":"api","process_generation_hint":"pid=48213"}`)
		}},
		{"session id is not a uuid", func(f *museFixture) {
			f.writeFile(filepath.Join(museRuntimeDir, "..evil.json"),
				`{"session_id":"..evil","workspace_label":"api","process_generation_hint":"pid=48213"}`)
		}},
		{"oversized runtime file", func(f *museFixture) {
			f.writeFile(filepath.Join(museRuntimeDir, museTestID+".json"),
				`{"session_id":"`+museTestID+`","workspace_label":"api","process_generation_hint":"pid=48213","pad":"`+
					strings.Repeat("x", museRuntimeMax)+`"}`)
		}},
		{"hint without pid", func(f *museFixture) {
			f.writeFile(filepath.Join(museRuntimeDir, museTestID+".json"),
				`{"session_id":"`+museTestID+`","workspace_label":"api","process_generation_hint":"generation=3"}`)
		}},
		{"no log", func(f *museFixture) {
			f.runtime(museTestID, museTestPID, museTestLabel)
			f.log("2026/10/06", museTestOther, museTurn(museTestRun, "the answer")...)
		}},
		{"no data directory", func(f *museFixture) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMuseFixture(t)
			tc.setup(f)
			if tc.name != "no log" {
				f.log("2026/10/06", museTestID, museTurn(museTestRun, "the answer")...)
			}
			if tc.name == "no data directory" {
				if err := os.RemoveAll(filepath.Join(f.data, "runtime")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.read()
			if !errors.Is(err, domain.ErrNoReply) || errors.Is(err, domain.ErrReplyPending) {
				t.Fatalf("err = %v, want plain ErrNoReply", err)
			}
			if strings.Contains(err.Error(), museTestSecret) || strings.Contains(err.Error(), f.home) {
				t.Fatalf("error leaks: %v", err)
			}
			f.assertLogClean()
		})
	}
}

func TestMuseLastReplyRefusesLinkedLog(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	target := filepath.Join(f.home, "elsewhere.jsonl")
	if err := os.WriteFile(target, []byte(strings.Join(museTurn(museTestRun, "the answer"), "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.data, museSessionsDir, "2026", "10", "06", museTestID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, museLogName)); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := f.read(); !errors.Is(err, domain.ErrNoReply) {
		t.Fatalf("err = %v, want ErrNoReply", err)
	}
}

func TestMuseLastReplyWalkLimit(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/01/01", museTestID, museTurn(museTestRun, "the answer")...)
	for day := 2; day <= 20; day++ {
		if err := os.MkdirAll(filepath.Join(f.data, museSessionsDir, "2026", "01", strconv.Itoa(10+day)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := museWalkLimit
	museWalkLimit = 10
	t.Cleanup(func() { museWalkLimit = old })

	if _, err := f.read(); !errors.Is(err, domain.ErrNoReply) {
		t.Fatalf("err = %v, want ErrNoReply", err)
	}
	museWalkLimit = old
	if r, err := f.read(); err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
}

func TestMuseLastReplyCacheFollowsAMovedLog(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	first := f.log("2026/10/06", museTestID, museTurn(museTestRun, "first")...)
	if r, err := f.read(); err != nil || r.Text != "first" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	f.log("2026/10/07", museTestID, museTurn(museTestRun, "the answer")...)
	// The cached path is gone: the second read walks again.
	if r, err := f.read(); err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	if strings.Contains(f.logBuf.String(), `"cached":true`) {
		t.Fatalf("a stale cache entry was used: %s", f.logBuf.String())
	}
	if r, err := f.read(); err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
	if !strings.Contains(f.logBuf.String(), `"cached":true`) {
		t.Fatalf("cache never hit: %s", f.logBuf.String())
	}
}

func TestMuseLastReplyFallbackDataDir(t *testing.T) {
	f := newMuseFixture(t)
	f.data = filepath.Join(f.home, ".muse")
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.log("2026/10/06", museTestID, museTurn(museTestRun, "the answer")...)

	if r, err := f.read(); err != nil || r.Text != "the answer" {
		t.Fatalf("reply = %q, %v", r.Text, err)
	}
}

func TestMuseLastReplyBudget(t *testing.T) {
	f := newMuseFixture(t)
	f.runtime(museTestID, museTestPID, museTestLabel)
	f.reader.maxScan = 1 << 20
	huge := museLine(museT1, museTestRun, map[string]any{"kind": "tool_output", "text": museTestSecret + strings.Repeat("x", 2<<20)})
	f.log("2026/10/06", museTestID, append(museTurn("run-old", "old answer"), museStarted(museT1, museTestRun), huge)...)

	_, err := f.read()
	if !errors.Is(err, domain.ErrNoReply) || errors.Is(err, domain.ErrReplyPending) {
		t.Fatalf("err = %v, want a plain budget miss", err)
	}
	f.assertLogClean()
}

func TestMuseLastReplyOtherKindAndCancel(t *testing.T) {
	f := newMuseFixture(t)
	agent := f.agent()
	agent.Kind = "codex"
	if _, err := f.reader.LastReply(context.Background(), agent); !errors.Is(err, domain.ErrNoReply) ||
		!strings.Contains(err.Error(), "unsupported agent") {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.reader.LastReply(ctx, f.agent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
