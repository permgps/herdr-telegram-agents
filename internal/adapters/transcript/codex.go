package transcript

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// kindCodex is the Herdr agent kind CodexReader understands.
	kindCodex = "codex"
	// codexWalkLimitDefault is the value of codexWalkLimit.
	codexWalkLimitDefault = 50000
	// codexReadBatch is how many directory entries are read at a time.
	codexReadBatch = 256
	// codexMetaMax bounds the first record, read to check whose rollout a
	// file is. Codex's session_meta is a few tens of KB.
	codexMetaMax = 1 << 20
	// codexLaterDays is how many days after its creation a thread's later
	// rollout files are looked for.
	codexLaterDays = 366
	// codexHeadBlock is how much of the rollout's head is read at a time.
	codexHeadBlock = 32 << 10
)

// codexWalkLimit caps the directory entries visited by the search for a
// rollout file, the day-directory shortcut included, so a huge sessions tree
// or directory costs a screen post and not a long walk. A variable so a test can lower it.
var codexWalkLimit = codexWalkLimitDefault

// codexDir is the part of an open directory the search uses.
type codexDir interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

// codexOpenDir opens a directory below the sessions root. A variable so a
// test can make a directory fail part way through its listing.
var codexOpenDir = func(root *os.Root, dir string) (codexDir, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// codexHomeDir is Codex's data directory, relative to the user's home.
// CODEX_HOME is not honoured: environment access stays out of this
// package (see scripts/check-imports.sh).
var codexHomeDir = ".codex"

// codexThreadID is the only shape of session id used in a file name: Codex
// thread ids are lower-case UUIDs. Anything else, a path separator or ".."
// included, is refused before it can reach the file system.
var codexThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Markers a rollout line must contain before it is decoded. Tool outputs
// make single lines megabytes long; a line without one of these is never
// parsed.
var (
	codexMarkComplete = []byte(`"task_complete"`)
	codexMarkStarted  = []byte(`"task_started"`)
	codexMarkAborted  = []byte(`"turn_aborted"`)
	codexMarkRollback = []byte(`"thread_rolled_back"`)
	codexMarkContext  = []byte(`"turn_context"`)
	codexMarkUsage    = []byte(`"token_usage_record"`)
)

// CodexReader implements domain.ReplySource for Codex. Codex writes every
// thread to ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl,
// and Herdr reports the thread id as the pane's agent_session, so the
// reader opens exactly that file: no guessing from the working directory.
// The reply is the turn's task_complete record, which carries the final
// answer as last_agent_message.
//
// Like OpenCodeReader, the session tuple comes from Herdr at read time and
// is used for that one lookup: it is never stored or logged, and neither is
// the rollout path, which contains the thread id. A tuple whose digest
// differs from the topic's key means the pane now runs another session, so
// the reader answers ErrNoReply rather than post that session's reply into
// this topic.
type CodexReader struct {
	session func(ctx context.Context, paneID string) (domain.SessionTuple, error)
	home    func() (string, error)
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
}

// NewCodexReader wires the reader over Herdr's session lookup and the
// current user's home directory.
func NewCodexReader(session func(context.Context, string) (domain.SessionTuple, error), log *slog.Logger) *CodexReader {
	return newCodexReader(session, os.UserHomeDir, time.Now, log)
}

// newCodexReader takes the home and clock sources so tests can point the
// reader at a temporary directory.
func newCodexReader(session func(context.Context, string) (domain.SessionTuple, error),
	home func() (string, error), now func() time.Time, log *slog.Logger) *CodexReader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CodexReader{session: session, home: home, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply returns the final answer of the pane's last completed Codex
// turn. Every failure is domain.ErrNoReply wrapped with a reason that
// carries no session value and no path; the caller falls back to the
// screen. A turn that is still running or was interrupted has no answer.
func (r *CodexReader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindCodex {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrNoReply, agent.Kind)
	}
	tuple, err := r.session(ctx, agent.PaneID)
	if err != nil {
		return domain.Reply{}, classifySessionError(ctx, err, "session lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if tuple.Agent != kindCodex || tuple.Kind != "id" || tuple.Value == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports no codex session id for the pane", domain.ErrNoReply)
	}
	digest := tuple.Digest()
	if digest == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports an incomplete session for the pane", domain.ErrNoReply)
	}
	if agent.SessionDigest == "" || digest != agent.SessionDigest {
		return domain.Reply{}, fmt.Errorf("%w: the pane now runs another session", domain.ErrNoReply)
	}
	id := strings.ToLower(tuple.Value)
	if !codexThreadID.MatchString(id) {
		return domain.Reply{}, fmt.Errorf("%w: the codex session id is not a thread uuid", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no home directory", domain.ErrNoReply)
	}
	// The sessions directory itself may be a link (the user's choice); the
	// lookup below can never leave it.
	sessions, err := os.OpenRoot(filepath.Join(home, codexHomeDir, "sessions"))
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no rollout file for the codex session", domain.ErrNoReply)
	}
	defer sessions.Close()
	rel, seen, err := findCodexRollout(ctx, sessions, id, r.now())
	if err != nil {
		return domain.Reply{}, err
	}
	f, info, err := openCodexRollout(sessions, rel, seen, id)
	if err != nil {
		return domain.Reply{}, err
	}
	defer f.Close()
	text, meta, stats, err := codexLastReplyFrom(f, info.Size(), r.maxScan)
	if err != nil {
		return domain.Reply{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	written := meta.Ended
	if written.IsZero() {
		written = info.ModTime()
	}
	age := r.now().Sub(written)
	r.log.Debug("codex reply found", slog.String("pane", agent.PaneID),
		slog.Int("lines", stats.lines), slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped),
		slog.Int("chars", len(text)), slog.Int64("age_ms", age.Milliseconds()),
		slog.String("model", meta.Model), slog.Int("output_tokens", meta.OutputTokens))
	return domain.Reply{Text: text, Source: "codex rollout", Age: age, Written: written, Meta: meta}, nil
}

// classifySessionError turns a failed Herdr lookup (session tuple, pane
// processes) into ErrNoReply with a category, keeping cancellation as is so
// the caller stops instead of posting the screen.
func classifySessionError(ctx context.Context, err error, category string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %s", domain.ErrNoReply, category)
}

// codexRollout is a rollout file of the thread found under the sessions root.
type codexRollout struct {
	rel   string // path below the sessions root
	stamp string // the file name's timestamp, fixed width so it sorts as text
	rid   string // the rollout id: the thread id unless the thread was reverted
	ms    int64  // the rollout id's UUIDv7 time
	v7    bool   // whether the rollout id is a UUIDv7
}

// newerThan orders by the rollout id's UUIDv7 time, which is UTC: the file
// name's stamp is local wall time and goes backwards when daylight saving
// ends or the machine changes zone. The stamp and then the id only break a
// tie within one millisecond.
func (c codexRollout) newerThan(o codexRollout) bool {
	if c.ms != o.ms {
		return c.ms > o.ms
	}
	return c.stamp > o.stamp || c.stamp == o.stamp && c.rid > o.rid
}

// codexV7Millis returns the creation time in milliseconds a UUIDv7 carries in
// its first 48 bits; ok is false for any other UUID.
func codexV7Millis(id string) (ms int64, ok bool) {
	raw, err := hex.DecodeString(strings.ReplaceAll(id[:13], "-", ""))
	if err != nil || len(raw) != 6 || id[14] != '7' {
		return 0, false
	}
	for _, b := range raw {
		ms = ms<<8 | int64(b)
	}
	return ms, true
}

const codexStampLen = len("2006-01-02T15-04-05")

var codexStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}$`)

// codexRolloutName parses rollout-<local time>-<thread id>[_<rollout id>].jsonl
// for thread id. Codex adds the second id when it reverts a thread: the
// thread keeps its id but continues in a new file, and the old file stays.
func codexRolloutName(name, id string) (stamp, rid string, ok bool) {
	core, ok := strings.CutPrefix(name, "rollout-")
	if !ok {
		return "", "", false
	}
	core, ok = strings.CutSuffix(core, ".jsonl")
	if !ok || len(core) < codexStampLen+1 || core[codexStampLen] != '-' || !codexStamp.MatchString(core[:codexStampLen]) {
		return "", "", false
	}
	thread, rollout, reverted := strings.Cut(core[codexStampLen+1:], "_")
	if thread != id || reverted && !codexThreadID.MatchString(rollout) {
		return "", "", false
	}
	if !reverted {
		rollout = thread
	}
	return core[:codexStampLen], rollout, true
}

// codexSearch looks for the rollout files of one thread. Its entry budget is
// shared by the day-directory shortcut and the tree walk, and directories are
// read in batches so that neither the budget nor a cancellation waits for a
// huge directory to load.
type codexSearch struct {
	ctx  context.Context
	root *os.Root
	id   string
	left int
	best *codexRollout
	// found counts the thread's rollout files, and odd those whose id is not
	// a UUIDv7: with several files, such an id cannot be ordered.
	found, odd int
}

// scan reads dir, a path below the root, and offers every rollout file of the
// thread in it; with deep it goes into subdirectories too. Links are never
// followed and root refuses any path that leaves the tree.
//
// Only a directory that does not exist holds nothing. Anything the search
// cannot look into (a directory it may not read, a listing that fails part
// way, a link where a rollout or, in the walk, a directory could be) may hide
// the newest rollout, so the search is incomplete and must not answer with
// an older file.
func (s *codexSearch) scan(dir string, deep bool) error {
	d, err := codexOpenDir(s.root, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errCodexIncomplete
	}
	defer d.Close()
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		entries, readErr := d.ReadDir(codexReadBatch)
		for _, e := range entries {
			if s.left--; s.left < 0 {
				return errCodexWalkLimit
			}
			switch {
			case e.Type().IsRegular():
				s.offer(dir, e.Name())
			case s.names(e.Name()):
				return errCodexIncomplete // the thread's name on a link or a directory
			case deep && e.IsDir():
				if err := s.scan(filepath.Join(dir, e.Name()), true); err != nil {
					return err
				}
			case deep && e.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
				// A link, or a Windows junction, where a directory could be.
				return errCodexIncomplete
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errCodexIncomplete
		}
	}
}

// names reports whether name is that of a rollout file of the thread.
func (s *codexSearch) names(name string) bool {
	_, _, ok := codexRolloutName(name, s.id)
	return ok
}

func (s *codexSearch) offer(dir, name string) {
	stamp, rid, ok := codexRolloutName(name, s.id)
	if !ok {
		return
	}
	c := codexRollout{rel: filepath.Join(dir, name), stamp: stamp, rid: rid}
	c.ms, c.v7 = codexV7Millis(rid)
	s.found++
	if !c.v7 {
		s.odd++
	}
	if s.best == nil || c.newerThan(*s.best) {
		s.best = &c
	}
}

// findCodexRollout returns the path, below root, of the newest rollout file of
// thread id. Codex names it rollout-<local time>-<id>.jsonl inside the day
// directory of the thread's creation, and thread ids are UUIDv7, whose first
// 48 bits are that creation time in milliseconds: the days around it (in UTC
// and in local time, the file name's zone) and the days after it, up to now,
// are read first, then the whole tree if none of them holds a file. When
// those days do not reach now (a thread older than codexLaterDays, or an id
// that is not a UUIDv7), they cannot show which file is the newest and the
// whole tree is walked instead. Only a regular file whose name carries
// exactly the thread id matches, so a helper thread that shares an id prefix,
// or a link, never does.
//
// A reverted thread has several files, the later ones in the day directory of
// the revert, and Codex uses the newest: so does the reader, among all the
// days it reads. Reading them all costs the entries of those days, against
// the same budget as the rest of the search; when the budget runs out, or a
// directory cannot be read, the search fails rather than answer from a file
// that may not be the newest.
//
// It also returns what the file looked like at that moment, for
// openCodexRollout to compare with what it opens.
func findCodexRollout(ctx context.Context, root *os.Root, id string, now time.Time) (string, os.FileInfo, error) {
	s := &codexSearch{ctx: ctx, root: root, id: id, left: codexWalkLimit}
	var err error
	dirs, complete := codexDayDirs(id, now)
	if complete {
		for _, dir := range dirs {
			if err = s.scan(dir, false); err != nil {
				break
			}
		}
	}
	if err == nil && (!complete || s.best == nil) {
		err = s.scan(".", true)
	}
	switch {
	case err == nil && s.best != nil && s.found > 1 && s.odd > 0:
		return "", nil, fmt.Errorf("%w: the newest rollout cannot be told apart", domain.ErrNoReply)
	case err == nil && s.best != nil:
		seen, lerr := root.Lstat(s.best.rel)
		if lerr != nil || !seen.Mode().IsRegular() {
			return "", nil, fmt.Errorf("%w: rollout could not be opened", domain.ErrNoReply)
		}
		return s.best.rel, seen, nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", nil, err
	case errors.Is(err, errCodexWalkLimit):
		return "", nil, fmt.Errorf("%w: too many session files to search", domain.ErrNoReply)
	case errors.Is(err, errCodexIncomplete):
		return "", nil, fmt.Errorf("%w: a session directory could not be read", domain.ErrNoReply)
	}
	return "", nil, fmt.Errorf("%w: no rollout file for the codex session", domain.ErrNoReply)
}

var (
	errCodexWalkLimit  = errors.New("codex sessions walk limit")
	errCodexIncomplete = errors.New("codex sessions search incomplete")
)

// openCodexRollout opens the file the search chose, without blocking on a
// FIFO, and checks it is the one the search saw (seen) and that it is the
// thread's: a regular file, the same file as at lookup time, and whose first
// record is the session_meta of thread id. The name alone does not say whose
// rollout a file is.
func openCodexRollout(root *os.Root, rel string, seen os.FileInfo, id string) (*os.File, os.FileInfo, error) {
	f, info, err := openRegularSeen(root, rel, seen)
	if err != nil {
		return nil, nil, openFailure("rollout", err)
	}
	if err := codexCheckThread(codexReadAt{f}, info.Size(), id); err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// codexCheckThread requires the rollout's first record to be the session_meta
// of thread id. Codex writes it first in every rollout, and its id is the
// thread's own (a reverted thread's new file keeps it).
func codexCheckThread(f io.ReaderAt, size int64, id string) error {
	var head []byte
	for off := int64(0); off < min(size, codexMetaMax+1) && !bytes.Contains(head, []byte{'\n'}); {
		block := make([]byte, min(size-off, codexHeadBlock))
		n, err := f.ReadAt(block, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: the rollout could not be read", domain.ErrNoReply)
		}
		if n == 0 {
			break
		}
		head = append(head, block[:n]...)
		off += int64(n)
	}
	line, _, found := bytes.Cut(head, []byte{'\n'})
	if !found && len(head) > codexMetaMax {
		return fmt.Errorf("%w: the rollout's first record is too long", domain.ErrNoReply)
	}
	if found && len(line) > codexMetaMax {
		return fmt.Errorf("%w: the rollout's first record is too long", domain.ErrNoReply)
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil || rec.Type != "session_meta" {
		return fmt.Errorf("%w: the rollout does not start with its session metadata", domain.ErrNoReply)
	}
	if !strings.EqualFold(rec.Payload.ID, id) {
		return fmt.Errorf("%w: the rollout belongs to another session", domain.ErrNoReply)
	}
	return nil
}

// codexDayDirs lists the session day directories worth trying first for
// thread id, as paths below the sessions root: the days around the UUIDv7
// creation time, UTC and local, then every later day up to until, where a
// reverted thread continues in a new file. complete reports whether the
// later days reach until; past codexLaterDays they do not, and a newer file
// may be in a day that is not listed, nor when until is before the creation
// day. A UUID that is not version 7 yields none and is never complete.
func codexDayDirs(id string, until time.Time) (dirs []string, complete bool) {
	ms, ok := codexV7Millis(id)
	if !ok {
		return nil, false
	}
	created := time.UnixMilli(ms)
	if until.Before(created.AddDate(0, 0, -1)) {
		// The clock is behind the thread's creation, so a later file may
		// sit in a day before it: only the whole tree can tell.
		return nil, false
	}
	seen := map[string]bool{}
	add := func(t time.Time) {
		for _, loc := range []*time.Location{time.UTC, time.Local} {
			d := t.In(loc)
			dir := filepath.Join(d.Format("2006"), d.Format("01"), d.Format("02"))
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
	}
	for _, shift := range []int{0, -1, 1} {
		add(created.AddDate(0, 0, shift))
	}
	for days := 2; days <= codexLaterDays; days++ {
		day := created.AddDate(0, 0, days)
		if day.After(until.AddDate(0, 0, 1)) {
			return dirs, true
		}
		add(day)
	}
	return dirs, !created.AddDate(0, 0, codexLaterDays).Before(until.AddDate(0, 0, 1))
}

// codexRecord is the slice of a rollout line the reader decodes. Codex
// writes many record types; only the turn boundaries, the turn context and
// the token usage are read.
type codexRecord struct {
	Type      string       `json:"type"`
	Timestamp string       `json:"timestamp"`
	Payload   codexPayload `json:"payload"`
}

type codexPayload struct {
	Type             string  `json:"type"`
	TurnID           string  `json:"turn_id"`
	LastAgentMessage *string `json:"last_agent_message"`
	StartedAt        int64   `json:"started_at"`
	NumTurns         *int64  `json:"num_turns"`
	CompletedAt      int64   `json:"completed_at"`
	Model            string  `json:"model"`
	TurnTokenUsage   struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"turn_token_usage"`
}

// codexLastReplyFrom walks a rollout of size bytes backwards. The first
// turn boundary it meets decides: a task_started means the turn is still
// running, a turn_aborted that it was interrupted and a thread_rolled_back
// that the newest turns were removed, none has an answer; a task_complete
// carries the answer. The walk then goes on to that turn's task_started for the model and the output tokens; when the
// budget or the file runs out first, the answer found is returned with the
// partial meta rather than an error.
func codexLastReplyFrom(f io.ReaderAt, size, budget int64) (string, domain.TurnMeta, scanStats, error) {
	f = codexReadAt{f}
	var stats scanStats
	var meta domain.TurnMeta
	// A record Codex is still writing has no newline yet: the newest turn
	// boundary may be that half line, and walking past it would answer with
	// the previous turn's reply.
	if size > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
			return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: the rollout could not be read", domain.ErrNoReply)
		}
		if last[0] != '\n' {
			return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: the rollout ends mid-record", domain.ErrNoReply)
		}
	}
	var text, turnID string
	var haveOutcome, haveTokens bool
	visit := func(line []byte) error {
		stats.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil
		}
		if !haveOutcome {
			if !bytes.Contains(line, codexMarkComplete) && !bytes.Contains(line, codexMarkStarted) && !bytes.Contains(line, codexMarkAborted) && !bytes.Contains(line, codexMarkRollback) {
				return nil
			}
		} else if !bytes.Contains(line, codexMarkUsage) && !bytes.Contains(line, codexMarkContext) && !bytes.Contains(line, codexMarkStarted) {
			return nil
		}
		var rec codexRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if !haveOutcome {
				// The newest turn boundary cannot be read (a changed field
				// type, say): skipping it would answer with an older turn.
				return fmt.Errorf("%w: the newest codex turn record is unreadable", domain.ErrNoReply)
			}
			stats.skipped++
			return nil
		}
		p := rec.Payload
		if !haveOutcome {
			if rec.Type != "event_msg" {
				return nil
			}
			switch p.Type {
			case "task_started":
				return fmt.Errorf("%w: the codex turn is still running", domain.ErrNoReply)
			case "turn_aborted":
				return fmt.Errorf("%w: the last codex turn was interrupted", domain.ErrNoReply)
			case "thread_rolled_back":
				// Codex drops the newest user turns when it replays a
				// rollback, so a rollback newer than the last task_complete
				// removed that turn: its answer is no longer part of the
				// conversation. Only an explicit zero is a no-op.
				if p.NumTurns != nil && *p.NumTurns == 0 {
					return nil
				}
				return fmt.Errorf("%w: the last codex turn was rolled back", domain.ErrNoReply)
			case "task_complete":
				if p.LastAgentMessage == nil || strings.TrimSpace(*p.LastAgentMessage) == "" {
					return fmt.Errorf("%w: the last codex turn has no final answer", domain.ErrNoReply)
				}
				haveOutcome = true
				text, turnID = strings.TrimSpace(*p.LastAgentMessage), p.TurnID
				meta.Started = codexUnix(p.StartedAt)
				meta.Ended = codexUnix(p.CompletedAt)
				if meta.Ended.IsZero() {
					meta.Ended = parseStamp(rec.Timestamp)
				}
			}
			return nil
		}
		switch {
		case rec.Type == "token_usage_record" && p.TurnID == turnID && !haveTokens:
			meta.OutputTokens, haveTokens = p.TurnTokenUsage.OutputTokens, true
		case rec.Type == "turn_context" && p.TurnID == turnID && p.Model != "" && meta.Model == "":
			meta.Model = p.Model
		case rec.Type == "event_msg" && p.Type == "task_started" && p.TurnID == turnID:
			if meta.Started.IsZero() {
				meta.Started = codexUnix(p.StartedAt)
			}
			return errStop
		}
		return nil
	}
	bytesRead, err := walkBack(f, size, budget, visit)
	stats.bytes = bytesRead
	switch {
	case errors.Is(err, errStop):
		return text, meta, stats, nil
	case haveOutcome:
		// The turn start is beyond the budget, the file start or a read
		// failure: the answer found is worth more than the missing meta.
		stats.readErr = err
		return text, meta, stats, nil
	case err != nil:
		return "", domain.TurnMeta{}, stats, err
	case bytesRead >= budget && size > budget:
		return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: no turn boundary within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: the rollout has no completed turn", domain.ErrNoReply)
}

// codexReadAt hides the reason of a failed read: a *fs.PathError carries the
// rollout path, which contains the thread id, and walkBack copies the error
// text into its own. Callers see only that the read failed.
type codexReadAt struct{ io.ReaderAt }

func (r codexReadAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, errors.New("read failed")
	}
	return n, err
}

// codexUnix converts Codex's second timestamps; zero stays zero.
func codexUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
