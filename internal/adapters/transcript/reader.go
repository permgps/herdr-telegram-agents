// Package transcript reads an agent's last reply from the session
// transcript the agent writes for itself, outside Herdr. Claude Code keeps
// one directory per project under ~/.claude/projects and one .jsonl per
// session, named after the session id. When Herdr reports the pane's
// Claude session id the reader opens that file; otherwise it takes the
// newest file in the directory of the pane's working directory, the session
// that just finished, unless that file is the session of another pane.
// Whenever the reader cannot tell, the caller falls back to the screen.
package transcript

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// kindClaude is the Herdr agent kind this reader understands.
	kindClaude = "claude"
	// transcriptSuffix is the extension of Claude Code session files.
	transcriptSuffix = ".jsonl"
	// defaultMaxScan bounds how many bytes are read from the end of a
	// transcript before giving up: a turn with huge tool results in it
	// costs one screen post, not a multi-megabyte parse.
	defaultMaxScan = 4 << 20
)

// projectsDir is Claude Code's transcript root, relative to the home.
var projectsDir = []string{".claude", "projects"}

// claudeSessionID is the shape of a Claude Code session id, the name of its
// transcript file: a lower-case UUID. Anything else, a path separator or
// ".." included, is refused before it can reach the file system.
var claudeSessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Reader implements domain.ReplySource for Claude Code transcripts.
type Reader struct {
	// sessions lists the agent_session tuple of every pane Herdr knows
	// one for, by pane id; nil reads by working directory alone. The
	// tuples are used for one lookup and never stored.
	sessions func(context.Context) (map[string]domain.SessionTuple, error)
	home     func() (string, error)
	now      func() time.Time
	log      *slog.Logger
	maxScan  int64
}

// NewReader returns a reader over the current user's home directory and
// Herdr's session list.
func NewReader(sessions func(context.Context) (map[string]domain.SessionTuple, error), log *slog.Logger) *Reader {
	return newReader(sessions, os.UserHomeDir, time.Now, log)
}

// newReader takes the session, home and clock sources so tests can point
// the reader at a temporary directory.
func newReader(sessions func(context.Context) (map[string]domain.SessionTuple, error), home func() (string, error), now func() time.Time, log *slog.Logger) *Reader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reader{sessions: sessions, home: home, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply finds the agent's transcript and returns the last text the
// agent wrote after the operator's last prompt. Every failure is
// domain.ErrNoReply wrapped with the reason; the reader logs the lookup at
// debug and leaves the one info line to the caller.
func (r *Reader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindClaude {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, agent.Kind)
	}
	if strings.TrimSpace(agent.Cwd) == "" {
		return domain.Reply{}, fmt.Errorf("%w: agent has no working directory", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: home directory: %v", domain.ErrNoReply, err)
	}
	dir := filepath.Join(append([]string{home}, append(projectsDir, projectSlug(agent.Cwd))...)...)
	path, modTime, candidates, err := r.pick(ctx, agent, dir)
	if err != nil {
		return domain.Reply{}, err
	}
	age := r.now().Sub(modTime)
	r.log.Debug("transcript lookup",
		slog.String("pane", agent.PaneID), slog.String("cwd", agent.Cwd), slog.String("dir", dir),
		slog.Int("candidates", candidates), slog.String("chosen", filepath.Base(path)), slog.Int64("age_ms", age.Milliseconds()))
	text, turn, stats, err := lastReplyIn(path, r.maxScan)
	meta := turn.meta()
	turnDuration, _ := meta.Duration()
	r.log.Debug("transcript scanned",
		slog.String("chosen", filepath.Base(path)), slog.Int("lines", stats.lines),
		slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped), slog.Bool("found", err == nil),
		slog.String("model", meta.Model), slog.Int("output_tokens", meta.OutputTokens), slog.Int("files", len(meta.Files)),
		slog.Int64("turn_ms", turnDuration.Milliseconds()), slog.Bool("complete", turn.complete))
	if err != nil {
		return domain.Reply{}, err
	}
	if stats.readErr != nil {
		r.log.Debug("[FIX] transcript read failed after the reply was found, stats are partial",
			slog.String("chosen", filepath.Base(path)), slog.Int64("bytes", stats.bytes), slog.String("err", stats.readErr.Error()))
	}
	return domain.Reply{Text: text, Source: path, Age: age, Written: modTime, Meta: meta}, nil
}

// pick chooses the agent's transcript in dir. A pane whose Claude session
// id Herdr reports reads that session's file and no other: the newest file
// may be another pane's in the same directory. A pane without one gets the
// newest file, unless that file is the session Herdr reports for another
// pane; that answer is the other pane's, and ErrStaleTranscript marks the
// fallback as expected (two panes in one directory), not a broken reader.
func (r *Reader) pick(ctx context.Context, agent domain.Agent, dir string) (path string, modTime time.Time, candidates int, err error) {
	own, others, err := r.claims(ctx, agent)
	if err != nil {
		return "", time.Time{}, 0, err
	}
	if own != "" {
		path = filepath.Join(dir, own+transcriptSuffix)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", time.Time{}, 0, fmt.Errorf("%w: no transcript for the pane's session in %s", domain.ErrNoReply, dir)
		}
		return path, info.ModTime(), 1, nil
	}
	path, modTime, candidates, err = newestTranscript(dir)
	if err != nil {
		return "", time.Time{}, 0, err
	}
	if others[strings.TrimSuffix(filepath.Base(path), transcriptSuffix)] {
		return "", time.Time{}, 0, fmt.Errorf("%w: %w: the newest transcript in %s is another pane's session",
			domain.ErrStaleTranscript, domain.ErrNoReply, dir)
	}
	return path, modTime, candidates, nil
}

// claims returns the Claude session id Herdr reports for the agent's pane
// and the set of ids it reports for every other pane. A failed lookup reads
// by directory alone, as before Herdr reported sessions; a pane whose
// session no longer matches the topic's key runs another session, and its
// reply would land in the wrong topic.
func (r *Reader) claims(ctx context.Context, agent domain.Agent) (own string, others map[string]bool, err error) {
	if r.sessions == nil {
		return "", nil, nil
	}
	tuples, err := r.sessions(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, ctxErr
		}
		r.log.Debug("claude session lookup failed, reading by directory",
			slog.String("pane", agent.PaneID), slog.String("err", err.Error()))
		return "", nil, nil
	}
	for pane, t := range tuples {
		if t.Agent != kindClaude || t.Kind != "id" || !claudeSessionID.MatchString(t.Value) {
			continue
		}
		if pane != agent.PaneID {
			if others == nil {
				others = make(map[string]bool)
			}
			others[t.Value] = true
			continue
		}
		if agent.SessionDigest != "" && t.Digest() != agent.SessionDigest {
			return "", nil, fmt.Errorf("%w: the pane now runs another session", domain.ErrNoReply)
		}
		own = t.Value
	}
	return own, others, nil
}

// newestTranscript returns the most recently modified session file in dir
// and how many candidates there were.
func newestTranscript(dir string) (path string, modTime time.Time, candidates int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", time.Time{}, 0, fmt.Errorf("%w: no transcript directory %s", domain.ErrNoReply, dir)
		}
		return "", time.Time{}, 0, fmt.Errorf("%w: read %s: %v", domain.ErrNoReply, dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), transcriptSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates++
		if path == "" || info.ModTime().After(modTime) {
			path, modTime = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if path == "" {
		return "", time.Time{}, 0, fmt.Errorf("%w: no transcript files in %s", domain.ErrNoReply, dir)
	}
	return path, modTime, candidates, nil
}
