package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// TestReplySourcesKeepsOpenCodePending runs the daemon's real reader chain
// for an OpenCode pane: a turn whose newest record is tool work must reach
// the caller as ErrReplyPending, not as the Codex reader's "unsupported
// agent" that comes after it (the regression in the closed PR #30); a
// settled turn returns its reply.
func TestReplySourcesKeepsOpenCodePending(t *testing.T) {
	tuple := domain.SessionTuple{Source: "herdr:opencode", Agent: "opencode", Kind: "id", Value: "ses_abc"}
	session := func(context.Context, string) (domain.SessionTuple, error) { return tuple, nil }
	agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1", SessionDigest: tuple.Digest()}, Kind: "opencode", Cwd: t.TempDir()}
	for name, tc := range map[string]struct {
		export    string
		wantText  string
		wantError error
	}{
		"tool last": {export: `{"info":{"id":"ses_abc"},"messages":[
			{"info":{"role":"user","time":{"created":1}},"parts":[{"type":"text","text":"go"}]},
			{"info":{"role":"assistant","time":{"created":2}},"parts":[{"type":"text","text":"checking"},{"type":"tool","tool":"bash"}]}]}`,
			wantError: domain.ErrReplyPending},
		"settled": {export: `{"info":{"id":"ses_abc"},"messages":[
			{"info":{"role":"user","time":{"created":1}},"parts":[{"type":"text","text":"go"}]},
			{"info":{"role":"assistant","time":{"created":2,"completed":3}},"parts":[{"type":"tool","tool":"bash"},{"type":"text","text":"All done."}]}]}`,
			wantText: "All done."},
	} {
		t.Run(name, func(t *testing.T) {
			export := func(context.Context, string) ([]byte, error) { return []byte(tc.export), nil }
			r, err := replySources(session, export, noProcesses, nil, nil).LastReply(context.Background(), agent)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("err = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err != nil || r.Text != tc.wantText {
				t.Fatalf("LastReply = %q, %v; want %q", r.Text, err, tc.wantText)
			}
		})
	}
}

// TestReplySourcesReadsAgy runs the daemon's real reader chain for an
// Antigravity pane: the agy reader is in the chain, a finished turn returns
// its answer and a running one reaches the caller as ErrReplyPending.
func TestReplySourcesReadsAgy(t *testing.T) {
	const id = "88276862-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tuple := domain.SessionTuple{Source: "herdr:antigravity_cli", Agent: "agy", Kind: "id", Value: id}
	session := func(context.Context, string) (domain.SessionTuple, error) { return tuple, nil }
	agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1", SessionDigest: tuple.Digest()}, Kind: "agy", Cwd: t.TempDir()}
	noExport := func(context.Context, string) ([]byte, error) { return nil, errors.New("not opencode") }
	prompt := `{"type":"USER_INPUT","source":"USER","content":"go","created_at":"2026-10-06T13:00:00-05:00"}`
	for name, tc := range map[string]struct {
		lines     []string
		wantText  string
		wantError error
	}{
		"tool last": {lines: []string{prompt,
			`{"type":"GENERIC","source":"MODEL","content":"The command exited with code 0.","created_at":"2026-10-06T13:00:01-05:00"}`},
			wantError: domain.ErrReplyPending},
		"settled": {lines: []string{prompt,
			`{"type":"PLANNER_RESPONSE","source":"MODEL","content":"All done.","created_at":"2026-10-06T13:00:02-05:00"}`},
			wantText: "All done."},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			dir := filepath.Join(home, ".gemini", "antigravity-cli", "brain", id, ".system_generated", "logs")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			r, err := replySources(session, noExport, noProcesses, nil, nil).LastReply(context.Background(), agent)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("err = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err != nil || r.Text != tc.wantText {
				t.Fatalf("LastReply = %q, %v; want %q", r.Text, err, tc.wantText)
			}
		})
	}
}

// TestReplySourcesReadsPi runs the daemon's real reader chain for a pi pane:
// a finished turn returns its answer, and a running one reaches the caller
// as ErrReplyPending rather than as another reader's "unsupported agent"
// (the Muse reader comes after it).
func TestReplySourcesReadsPi(t *testing.T) {
	noExport := func(context.Context, string) ([]byte, error) { return nil, errors.New("not opencode") }
	header := `{"type":"session","version":3,"id":"0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee","timestamp":"2026-10-07T09:00:00.000Z","cwd":"/work"}`
	prompt := `{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-07T09:00:01.000Z","message":{"role":"user","content":"go"}}`
	for name, tc := range map[string]struct {
		lines     []string
		wantText  string
		wantError error
	}{
		"tool last": {lines: []string{header, prompt,
			`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-07T09:00:02.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse"}}`},
			wantError: domain.ErrReplyPending},
		"settled": {lines: []string{header, prompt,
			`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-07T09:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"All done."}],"stopReason":"stop"}}`},
			wantText: "All done."},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions", "--work--", "2026-10-07T09-00-00-000Z_s.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tuple := domain.SessionTuple{Source: "herdr:pi", Agent: "pi", Kind: "path", Value: path}
			session := func(context.Context, string) (domain.SessionTuple, error) { return tuple, nil }
			agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1", SessionDigest: tuple.Digest()}, Kind: "pi", Cwd: t.TempDir()}
			r, err := replySources(session, noExport, noProcesses, nil, nil).LastReply(context.Background(), agent)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("err = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err != nil || r.Text != tc.wantText {
				t.Fatalf("LastReply = %q, %v; want %q", r.Text, err, tc.wantText)
			}
		})
	}
}

// noProcesses is the pane-process lookup for chain tests that are not
// about Muse.
func noProcesses(context.Context, string) ([]int, error) { return nil, errors.New("not muse") }

// TestReplySourcesReadsMuse runs the daemon's real reader chain for a Muse
// pane: the Muse reader is last in the chain and finds the session through
// the pane's process id, a finished run returns its final answer, and a
// running one reaches the caller as ErrReplyPending.
func TestReplySourcesReadsMuse(t *testing.T) {
	const id = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	session := func(context.Context, string) (domain.SessionTuple, error) {
		return domain.SessionTuple{}, errors.New("herdr reports no session for muse")
	}
	noExport := func(context.Context, string) ([]byte, error) { return nil, errors.New("not opencode") }
	processes := func(context.Context, string) ([]int, error) { return []int{4242}, nil }
	agent := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Kind: "muse", Cwd: filepath.Join("/work", "api")}
	run := func(event string) string {
		return `{"recorded_at":1791291600000000,"payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1","event":` + event + `}}`
	}
	started := run(`{"kind":"started","prompt":"go"}`)
	for name, tc := range map[string]struct {
		lines     []string
		wantText  string
		wantError error
	}{
		"running": {lines: []string{started,
			run(`{"kind":"assistant_message_committed","message_id":"m1","phase":"commentary","text":"checking"}`)},
			wantError: domain.ErrReplyPending},
		"settled": {lines: []string{started,
			run(`{"kind":"assistant_message_committed","message_id":"m1","phase":"commentary","text":"checking"}`),
			run(`{"kind":"assistant_message_committed","message_id":"m2","phase":"final_answer","text":"All done."}`),
			run(`{"kind":"terminal","terminal":"completed","reason":null}`)},
			wantText: "All done."},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			data := filepath.Join(home, ".local", "share", "muse")
			runtime := filepath.Join(data, "runtime", "muse", "sessions")
			logDir := filepath.Join(data, "sessions", "2026", "10", "07", id)
			for _, dir := range []string{runtime, logDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			rt := `{"schema_version":1,"session_id":"` + id + `","workspace_label":"api","process_generation_hint":"pid=4242"}`
			if err := os.WriteFile(filepath.Join(runtime, id+".json"), []byte(rt), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(logDir, "session.jsonl"), []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			r, err := replySources(session, noExport, processes, nil, nil).LastReply(context.Background(), agent)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("err = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err != nil || r.Text != tc.wantText {
				t.Fatalf("LastReply = %q, %v; want %q", r.Text, err, tc.wantText)
			}
		})
	}
}
