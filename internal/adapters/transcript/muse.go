package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// kindMuse is the Herdr agent kind MuseReader understands (Meta's Muse Code).
const kindMuse = "muse"

// museDataDirs are the candidate Muse data directories relative to the
// user's home, tried in this order. Only the first is confirmed (Linux,
// Muse Code 1.4.3); ".muse" is the museHome of the upstream SDK fixtures and
// the macOS location is a guess. Environment overrides such as
// XDG_DATA_HOME are not honoured (see scripts/check-imports.sh).
var museDataDirs = []string{
	filepath.Join(".local", "share", "muse"),
	".muse",
	filepath.Join("Library", "Application Support", "muse"),
}

// Paths below a Muse data directory: one runtime file per live session, and
// the durable logs under sessions/YYYY/MM/DD/<id>/session.jsonl, dated by the
// session's creation.
var (
	museRuntimeDir  = filepath.Join("runtime", "muse", "sessions")
	museSessionsDir = "sessions"
)

const museLogName = "session.jsonl"

const (
	// museRuntimeMax bounds one runtime file; they are a few hundred bytes.
	museRuntimeMax = 64 << 10
	// museRuntimeFiles bounds the runtime entries read per lookup.
	museRuntimeFiles = 256
	// museCandidates bounds the sessions whose log is looked up per reply.
	museCandidates = 8
	// museCacheMax bounds the session-to-log cache.
	museCacheMax = 64
	// museStartSlack is how much older than its pid's process a runtime
	// file may look and still belong to it: process start times and file
	// mtimes come from different clocks and precisions, and Muse writes the
	// runtime file after it starts, so only a clearly older file is stale.
	museStartSlack = 2 * time.Second
)

// museWalkLimit bounds the dated-directory walk that finds a session log:
// every directory entry seen and every Lstat counts. A variable for tests.
var museWalkLimit = 4000

// museSessionID is the only shape of session id used in a path: a UUID (v7
// for new sessions, older ones v4 or v5).
var museSessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// museHintPID reads the process id from process_generation_hint ("pid=NNN").
var museHintPID = regexp.MustCompile(`^pid=([0-9]{1,10})(?:[;, ].*)?$`)

// musePhaseName is the shape of a phase value safe to log: a fixed
// vocabulary word, never content.
var musePhaseName = regexp.MustCompile(`^[a-z_]{1,32}$`)

// museDateDir is the shape of the year, month and day directory names.
var museDateDir = regexp.MustCompile(`^[0-9]{2,4}$`)

// Markers a log line must contain before it is decoded: tool output makes
// single lines megabytes long, and only run events decide the answer.
var (
	museMarkRun       = []byte(`"run_id"`)
	museMarkCommitted = []byte(`"assistant_message_committed"`)
	museMarkTerminal  = []byte(`"terminal"`)
	museMarkStarted   = []byte(`"started"`)
)

// Run event kinds and terminal values, from the upstream Muse Code SDK
// (meta-models/muse-code-sdk, schema/msp/transcripts): a run opens with
// "started", streams "text_delta", commits whole messages and ends with one
// "terminal" whose value is completed, failed or cancelled.
const (
	museEventStarted   = "started"
	museEventCommitted = "assistant_message_committed"
	museEventTerminal  = "terminal"
	museTerminalDone   = "completed"
	musePhaseComment   = "commentary"
)

// MuseReader implements domain.ReplySource for Muse Code. Herdr reports no
// agent_session for Muse, so the pane is tied to its session through the
// pane's process ids: Muse keeps a runtime file per live session whose
// process_generation_hint names the owning pid, and the session id leads to
// the durable log. Pids are reused, so the pid is tied to its process start
// time: a runtime file written clearly before that process started was left
// by an earlier process with the same pid and is skipped. A pane is never
// matched by its directory name alone (~/a/api and ~/b/api collide, and so
// do two Muse panes in one directory); the directory name is a second guard
// against a reused pid, for when the start time is unknown.
//
// Only the final text of the newest finished run is posted: commentary is
// never posted, a running run answers domain.ErrReplyPending, and a failed
// or cancelled one domain.ErrNoReply. Like CodexReader, no session id, path,
// pid value or text reaches a log line or an error.
type MuseReader struct {
	processes func(ctx context.Context, paneID string) ([]int, error)
	started   func(pid int) (time.Time, bool)
	home      func() (string, error)
	now       func() time.Time
	log       *slog.Logger
	maxScan   int64

	mu   sync.Mutex
	logs map[string]string // session id → log path below the data directory
}

// NewMuseReader wires the reader over Herdr's pane process lookup, the
// process start time lookup and the current user's home directory. started
// reports false when a pid's start time is unknown; a nil started never
// knows it.
func NewMuseReader(processes func(context.Context, string) ([]int, error), started func(int) (time.Time, bool),
	log *slog.Logger) *MuseReader {
	return newMuseReader(processes, started, os.UserHomeDir, time.Now, log)
}

// newMuseReader takes the home and clock sources so tests can point the
// reader at a temporary directory.
func newMuseReader(processes func(context.Context, string) ([]int, error), started func(int) (time.Time, bool),
	home func() (string, error), now func() time.Time, log *slog.Logger) *MuseReader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if started == nil {
		started = func(int) (time.Time, bool) { return time.Time{}, false }
	}
	return &MuseReader{processes: processes, started: started, home: home, now: now, log: log,
		maxScan: defaultMaxScan, logs: make(map[string]string)}
}

// LastReply returns the final answer of the newest finished run of the
// pane's Muse session. Every failure is domain.ErrNoReply wrapped with a
// reason that carries no session value and no path; a running run is
// domain.ErrReplyPending.
func (r *MuseReader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindMuse {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, agent.Kind)
	}
	if agent.Cwd == "" {
		return domain.Reply{}, fmt.Errorf("%w: the muse pane has no working directory", domain.ErrNoReply)
	}
	pids, err := r.processes(ctx, agent.PaneID)
	if err != nil {
		return domain.Reply{}, classifySessionError(ctx, err, "pane process lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if len(pids) == 0 {
		return domain.Reply{}, fmt.Errorf("%w: no process in the pane", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no home directory", domain.ErrNoReply)
	}
	root, err := museOpenData(home)
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no muse data directory", domain.ErrNoReply)
	}
	defer root.Close()
	ids, match := r.sessionsFor(root, pids, filepath.Base(agent.Cwd), agent.PaneID)
	if len(ids) == 0 {
		r.log.Debug("muse session matched", slog.String("pane", agent.PaneID), slog.Int("runtime_files", match.files),
			slog.Int("skipped", match.skipped), slog.Int("candidates", 0), slog.Int("pids", len(pids)))
		return domain.Reply{}, fmt.Errorf("%w: no muse session for the pane", domain.ErrNoReply)
	}
	rel, err := r.newestLog(ctx, root, ids, agent.PaneID)
	if err != nil {
		return domain.Reply{}, err
	}
	chosenBy := "single"
	if len(ids) > 1 {
		chosenBy = "newest_log"
	}
	r.log.Debug("muse session matched", slog.String("pane", agent.PaneID), slog.Int("runtime_files", match.files),
		slog.Int("skipped", match.skipped), slog.Int("candidates", len(ids)), slog.Int("pids", len(pids)),
		slog.String("chosen_by", chosenBy))
	return r.readLog(ctx, root, rel, agent.PaneID)
}

// newestLog finds the log of each candidate session and keeps the most
// recently written one. Every candidate belongs to a process in this pane,
// so several only means the process started a fresh session (/new, /clear).
func (r *MuseReader) newestLog(ctx context.Context, root *os.Root, ids []string, pane string) (string, error) {
	var best string
	var bestTime time.Time
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rel, err := r.logFor(root, id, pane)
		if err != nil {
			continue
		}
		info, err := root.Lstat(rel)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best, bestTime = rel, info.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf("%w: muse session log not found", domain.ErrNoReply)
	}
	return best, nil
}

// readLog opens the chosen log and reads its newest finished run.
func (r *MuseReader) readLog(ctx context.Context, root *os.Root, rel, pane string) (domain.Reply, error) {
	f, info, err := openRegular(root, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return domain.Reply{}, fmt.Errorf("%w: muse session log not found", domain.ErrNoReply)
		}
		return domain.Reply{}, openFailure("the muse session log", err)
	}
	defer f.Close()
	text, phase, meta, stats, err := museLastReplyFrom(f, info.Size(), r.maxScan)
	if errors.Is(err, domain.ErrReplyPending) {
		r.log.Debug("muse reply pending", slog.String("pane", pane), slog.String("reason", err.Error()))
		return domain.Reply{}, err
	}
	if err != nil {
		return domain.Reply{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if stats.readErr != nil {
		r.log.Debug("[FIX] muse run start not reached, meta partial", slog.String("pane", pane),
			slog.String("err", stats.readErr.Error()))
	}
	written := meta.Ended
	if written.IsZero() {
		written = info.ModTime()
	}
	age := r.now().Sub(written)
	r.log.Debug("muse reply found", slog.String("pane", pane), slog.Int("lines", stats.lines),
		slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped), slog.Int("chars", len(text)),
		slog.Int64("age_ms", age.Milliseconds()), slog.String("phase", phase))
	return domain.Reply{Text: text, Source: "muse session log", Age: age, Written: written, Meta: meta}, nil
}

// museOpenData opens the first candidate data directory that holds Muse's
// runtime session directory. The directory itself may be a link (the
// user's choice); lookups below it can never leave it.
func museOpenData(home string) (*os.Root, error) {
	for _, dir := range museDataDirs {
		root, err := os.OpenRoot(filepath.Join(home, dir))
		if err != nil {
			continue
		}
		if info, err := root.Lstat(museRuntimeDir); err == nil && info.IsDir() {
			return root, nil
		}
		root.Close()
	}
	return nil, fs.ErrNotExist
}

// museMatchStats describes one runtime directory scan, for the debug log.
type museMatchStats struct {
	files   int // runtime entries seen
	skipped int // entries refused: name, type, size or content
}

// museRuntime is the part of a runtime session file the reader uses.
type museRuntime struct {
	SessionID string `json:"session_id"`
	Workspace string `json:"workspace_label"`
	Hint      string `json:"process_generation_hint"`
}

// sessionsFor returns the ids of the runtime sessions owned by one of the
// pane's processes and labelled with the pane's directory name, at most
// museCandidates of them. A runtime file older than its pid's process is
// skipped.
func (r *MuseReader) sessionsFor(root *os.Root, pids []int, label, pane string) ([]string, museMatchStats) {
	var stats museMatchStats
	dir, err := root.Open(museRuntimeDir)
	if err != nil {
		return nil, stats
	}
	entries, _ := dir.ReadDir(museRuntimeFiles)
	dir.Close()
	var ids []string
	for _, entry := range entries {
		stats.files++
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !museSessionID.MatchString(id) {
			stats.skipped++
			continue
		}
		rt, written, ok := museReadRuntime(root, filepath.Join(museRuntimeDir, entry.Name()))
		if !ok || rt.SessionID != id {
			stats.skipped++
			continue
		}
		pid, ok := museHintPIDOf(rt.Hint)
		if !ok || !slices.Contains(pids, pid) || rt.Workspace != label {
			continue
		}
		if start, known := r.started(pid); known && written.Before(start.Add(-museStartSlack)) {
			r.log.Debug("muse runtime file predates its process, skipped", slog.String("pane", pane),
				slog.String("reason", "stale_pid"))
			continue
		}
		ids = append(ids, id)
		if len(ids) == museCandidates {
			break
		}
	}
	return ids, stats
}

// museReadRuntime reads one runtime file: regular, bounded, valid JSON. It
// also returns the file's mtime.
func museReadRuntime(root *os.Root, rel string) (museRuntime, time.Time, bool) {
	f, info, err := openRegular(root, rel)
	if err != nil {
		return museRuntime{}, time.Time{}, false
	}
	defer f.Close()
	if info.Size() > museRuntimeMax {
		return museRuntime{}, time.Time{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, museRuntimeMax+1))
	if err != nil || len(data) > museRuntimeMax {
		return museRuntime{}, time.Time{}, false
	}
	var rt museRuntime
	if err := json.Unmarshal(data, &rt); err != nil {
		return museRuntime{}, time.Time{}, false
	}
	return rt, info.ModTime(), true
}

// museHintPIDOf parses the owning pid from a process_generation_hint.
func museHintPIDOf(hint string) (int, bool) {
	m := museHintPID.FindStringSubmatch(hint)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// logFor returns the path of a session's durable log below the data
// directory. The log sits in the directory of the session's creation date,
// which the runtime file does not record, so the dated directories are
// walked newest first within museWalkLimit; a hit is cached per session id.
func (r *MuseReader) logFor(root *os.Root, id, pane string) (string, error) {
	r.mu.Lock()
	cached, ok := r.logs[id]
	r.mu.Unlock()
	if ok {
		if info, err := root.Lstat(cached); err == nil && info.Mode().IsRegular() {
			r.log.Debug("muse session log walk", slog.String("pane", pane), slog.Int("visited", 0), slog.Bool("cached", true))
			return cached, nil
		}
		r.mu.Lock()
		delete(r.logs, id)
		r.mu.Unlock()
	}
	rel, visited, err := museWalkLogs(root, id)
	r.log.Debug("muse session log walk", slog.String("pane", pane), slog.Int("visited", visited), slog.Bool("cached", false))
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if len(r.logs) >= museCacheMax {
		clear(r.logs)
	}
	r.logs[id] = rel
	r.mu.Unlock()
	return rel, nil
}

// museWalkLogs looks for sessions/<year>/<month>/<day>/<id>/session.jsonl,
// newest date first. It returns the path found and the entries visited.
func museWalkLogs(root *os.Root, id string) (string, int, error) {
	visited := 0
	var walk func(rel string, depth int) (string, error)
	walk = func(rel string, depth int) (string, error) {
		if depth == 3 {
			visited++
			if visited > museWalkLimit {
				return "", errMuseWalkLimit
			}
			candidate := filepath.Join(rel, id, museLogName)
			if info, err := root.Lstat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
			return "", fs.ErrNotExist
		}
		names, err := museDateNames(root, rel)
		if err != nil {
			return "", fs.ErrNotExist
		}
		for _, name := range names {
			visited++
			if visited > museWalkLimit {
				return "", errMuseWalkLimit
			}
			found, err := walk(filepath.Join(rel, name), depth+1)
			if err == nil || errors.Is(err, errMuseWalkLimit) {
				return found, err
			}
		}
		return "", fs.ErrNotExist
	}
	rel, err := walk(museSessionsDir, 0)
	if err != nil {
		return "", visited, fmt.Errorf("%w: muse session log not found", domain.ErrNoReply)
	}
	return rel, visited, nil
}

// errMuseWalkLimit ends the dated-directory walk once museWalkLimit entries
// were visited.
var errMuseWalkLimit = errors.New("walk limit reached")

// museDateNames lists the date-shaped directory names below rel, newest
// (largest) first.
func museDateNames(root *os.Root, rel string) ([]string, error) {
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && museDateDir.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	return names, nil
}

// museRecord is the part of one durable log line the reader uses.
type museRecord struct {
	RecordedAt int64 `json:"recorded_at"`
	Payload    struct {
		Kind  string `json:"kind"`
		RunID string `json:"run_id"`
		Event struct {
			Kind     string  `json:"kind"`
			Phase    *string `json:"phase"`
			Text     string  `json:"text"`
			Terminal string  `json:"terminal"`
		} `json:"event"`
	} `json:"payload"`
}

// museTerminalClass names a terminal value for an error without echoing
// an unknown one.
func museTerminalClass(terminal string) string {
	switch terminal {
	case "failed", "cancelled":
		return terminal
	}
	return "other"
}

// musePhaseClass names a phase for the debug log: absent, a known-shaped
// word, or "other".
func musePhaseClass(phase *string) string {
	switch {
	case phase == nil:
		return "none"
	case musePhaseName.MatchString(*phase):
		return *phase
	}
	return "other"
}

// museLastReplyFrom walks a durable log back from its end. The newest run
// event decides: a run that has not ended is still running
// (ErrReplyPending); a failed or cancelled one has no answer; a completed
// one answers with its newest committed message that is not commentary,
// and the walk goes on to the run's start for the start time. Events of
// other runs are ignored. When the budget or the file runs out after the
// answer was found, the answer is returned with partial meta. It also
// returns the answer's phase class, for the debug log only.
func museLastReplyFrom(f io.ReaderAt, size, budget int64) (string, string, domain.TurnMeta, scanStats, error) {
	f = codexReadAt{f}
	var stats scanStats
	var meta domain.TurnMeta
	if size == 0 {
		return "", "", meta, stats, fmt.Errorf("%w: the muse session log is empty", domain.ErrNoReply)
	}
	// A record Muse is still writing has no newline yet: walking past it
	// would answer with an older run.
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
		return "", "", meta, stats, fmt.Errorf("%w: the muse session log could not be read", domain.ErrNoReply)
	}
	if last[0] != '\n' {
		return "", "", meta, stats, fmt.Errorf("%w: the muse log ends mid-record", domain.ErrReplyPending)
	}
	var run, text, phase string
	visit := func(line []byte) error {
		stats.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.Contains(line, museMarkRun) {
			return nil
		}
		if !bytes.Contains(line, museMarkCommitted) && !bytes.Contains(line, museMarkTerminal) &&
			!bytes.Contains(line, museMarkStarted) {
			return nil
		}
		var rec museRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if run == "" {
				// The newest run event cannot be read: skipping it would
				// answer with an older run.
				return fmt.Errorf("%w: the newest muse record is unreadable", domain.ErrNoReply)
			}
			stats.skipped++
			return nil
		}
		p := rec.Payload
		if p.Kind != "run" || p.RunID == "" {
			return nil
		}
		if run == "" {
			return museDecide(p.Event.Kind, p.Event.Terminal, p.RunID, &run)
		}
		if p.RunID != run {
			return nil
		}
		switch p.Event.Kind {
		case museEventStarted:
			if text == "" {
				return fmt.Errorf("%w: no final answer in the muse run", domain.ErrNoReply)
			}
			meta.Started = museMicros(rec.RecordedAt)
			return errStop
		case museEventCommitted:
			if text != "" || (p.Event.Phase != nil && *p.Event.Phase == musePhaseComment) {
				return nil
			}
			if t := strings.TrimSpace(p.Event.Text); t != "" {
				text, phase = t, musePhaseClass(p.Event.Phase)
				meta.Ended = museMicros(rec.RecordedAt)
			}
		}
		return nil
	}
	bytesRead, err := walkBack(f, size, budget, visit)
	stats.bytes = bytesRead
	switch {
	case errors.Is(err, errStop):
		return text, phase, meta, stats, nil
	case text != "":
		// The run's start is beyond the budget, the file start or a read
		// failure: the answer found is worth more than the missing start.
		stats.readErr = err
		return text, phase, meta, stats, nil
	case err != nil:
		return "", "", domain.TurnMeta{}, stats, err
	case run != "":
		return "", "", domain.TurnMeta{}, stats, fmt.Errorf("%w: no final answer in the muse run", domain.ErrNoReply)
	case bytesRead >= budget && size > budget:
		return "", "", domain.TurnMeta{}, stats, fmt.Errorf("%w: no muse run record within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", "", domain.TurnMeta{}, stats, fmt.Errorf("%w: the muse session log has no run", domain.ErrNoReply)
}

// museDecide classifies the newest run event: only a completed terminal
// lets the walk go on, remembering its run.
func museDecide(kind, terminal, runID string, run *string) error {
	switch kind {
	case museEventStarted:
		return fmt.Errorf("%w: the muse run has no answer yet", domain.ErrReplyPending)
	case museEventCommitted:
		return fmt.Errorf("%w: the muse run is still running", domain.ErrReplyPending)
	case museEventTerminal:
		if terminal != museTerminalDone {
			return fmt.Errorf("%w: the muse run ended %s", domain.ErrNoReply, museTerminalClass(terminal))
		}
		*run = runID
	}
	return nil
}

// museMicros converts Muse's microsecond timestamps; zero stays zero.
func museMicros(us int64) time.Time {
	if us <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(us)
}
