package transcript

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestProjectSlug(t *testing.T) {
	tests := map[string]string{
		"/Users/alex/Projects/My/herdr_tg": "-Users-alex-Projects-My-herdr-tg",
		"/tmp/a.b c":                       "-tmp-a-b-c",
		"/Users/ор/проект":                 "-Users-" + strings.Repeat("-", 9), // ор(2) /(1) проект(6)
		"/x/😀":                             "-x---",
		"C:\\Work\\proj":                   "C--Work-proj",
		"":                                 "",
	}
	for in, want := range tests {
		if got := projectSlug(in); got != want {
			t.Errorf("projectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastReplyInFixtures(t *testing.T) {
	tests := []struct {
		file, want string
		skipped    int
		wantErr    string
	}{
		{file: "simple.jsonl", want: "Done: **all good**."},
		{file: "tool_last.jsonl", want: "Working on it."}, // the turn ended with a tool: the narration before it is the last text
		{file: "sidechain.jsonl", want: "Final."},
		{file: "meta_user.jsonl", want: "Answer"},
		{file: "noise.jsonl", want: "Noisy final", skipped: 1},
		{file: "empty.jsonl", wantErr: "transcript has no reply"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			text, stats, err := lastReplyIn(filepath.Join("testdata", tt.file), defaultMaxScan)
			if tt.wantErr != "" {
				if !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want ErrNoReply with %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if text != tt.want {
				t.Errorf("text = %q, want %q", text, tt.want)
			}
			if stats.skipped != tt.skipped || stats.lines == 0 || stats.bytes == 0 {
				t.Errorf("stats = %+v", stats)
			}
		})
	}
}

// writeTranscript builds a transcript in dir: a prompt, a text, a tool
// call with a result of payload bytes, then tail records.
func writeTranscript(t *testing.T, path string, payload int, tail ...string) {
	t.Helper()
	lines := []string{
		`{"type":"user","message":{"content":"big one"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Before big"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + strings.Repeat("x", payload) + `"}]}}`,
	}
	lines = append(lines, tail...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLastReplyInLargeToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, 2500*1024, `{"type":"assistant","message":{"content":[{"type":"text","text":"After big"}]}}`)
	text, stats, err := lastReplyIn(path, defaultMaxScan)
	if err != nil || text != "After big" {
		t.Fatalf("text = %q, err = %v", text, err)
	}
	if stats.bytes > blockSize*2 {
		t.Errorf("read %d bytes for a reply on the last line", stats.bytes)
	}
	// Without a text after the tool result the scan crosses the 2.5 MB line
	// (still within the budget) and finds the narration before the tool.
	writeTranscript(t, path, 2500*1024)
	text, stats, err = lastReplyIn(path, defaultMaxScan)
	if err != nil || text != "Before big" {
		t.Fatalf("text = %q, err = %v", text, err)
	}
	if stats.bytes < 2500*1024 {
		t.Errorf("scan stopped early: %d bytes", stats.bytes)
	}
	// A user record without a message is not a prompt boundary.
	lines := `{"type":"user","message":{"content":"q"}}` + "\n" + `{"type":"assistant","message":{"content":[{"type":"text","text":"kept"}]}}` + "\n" + `{"type":"user"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if text, _, err := lastReplyIn(path, defaultMaxScan); err != nil || text != "kept" {
		t.Fatalf("empty user record: %q %v", text, err)
	}
	// An assistant record with a plain string content is a reply too.
	lines = `{"type":"user","message":{"content":"q"}}` + "\n" + `{"type":"assistant","message":{"content":"plain string reply"}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if text, _, err := lastReplyIn(path, defaultMaxScan); err != nil || text != "plain string reply" {
		t.Fatalf("string content: %q %v", text, err)
	}
	// A prompt with nothing but tool traffic after it has no reply.
	lines = `{"type":"user","message":{"content":"go"}}` + "\n" + `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lastReplyIn(path, defaultMaxScan); !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), "after the last prompt") {
		t.Fatalf("err = %v", err)
	}
}

func TestLastReplyInBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, 3*1024*1024)
	_, _, err := lastReplyIn(path, 1<<20)
	if !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), "within the last") {
		t.Fatalf("err = %v, want the budget reason", err)
	}
}

func TestNewestTranscript(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.jsonl")
	fresh := filepath.Join(dir, "new.jsonl")
	for _, p := range []string{old, fresh, filepath.Join(dir, "notes.txt")} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(old, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, base.Add(time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	path, mod, n, err := newestTranscript(dir)
	if err != nil || path != fresh || n != 2 || !mod.Equal(base.Add(time.Hour)) {
		t.Fatalf("newestTranscript = %q %v %d %v", path, mod, n, err)
	}
	if _, _, _, err := newestTranscript(filepath.Join(dir, "missing")); !errors.Is(err, domain.ErrNoReply) {
		t.Errorf("missing dir err = %v", err)
	}
	empty := t.TempDir()
	if _, _, _, err := newestTranscript(empty); !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), "no transcript files") {
		t.Errorf("empty dir err = %v", err)
	}
}

func TestLastReply(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/op/Projects/demo"
	dir := filepath.Join(home, ".claude", "projects", projectSlug(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "simple.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	mod := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	now := mod.Add(3 * time.Second)
	r := newReader(func() (string, error) { return home, nil }, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Kind: "claude", Cwd: cwd}

	reply, err := r.LastReply(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "Done: **all good**." || reply.Source != path || reply.Age != 3*time.Second {
		t.Errorf("reply = %+v", reply)
	}

	cases := map[string]domain.Agent{
		"unsupported agent":       {Kind: "codex", Cwd: cwd},
		"no working directory":    {Kind: "claude"},
		"no transcript directory": {Kind: "claude", Cwd: "/elsewhere"},
	}
	for reason, a := range cases {
		_, err := r.LastReply(context.Background(), a)
		if !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), reason) {
			t.Errorf("%s: err = %v", reason, err)
		}
	}
	broken := newReader(func() (string, error) { return "", errors.New("no home") }, time.Now, nil)
	if _, err := broken.LastReply(context.Background(), agent); !errors.Is(err, domain.ErrNoReply) {
		t.Errorf("home failure err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.LastReply(ctx, agent); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx err = %v", err)
	}
}
