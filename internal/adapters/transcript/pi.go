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
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// kindPi is the Herdr agent kind PiReader understands (the pi coding agent).
const kindPi = "pi"

// piHeaderMax bounds the read of a session file's first line, the header.
const piHeaderMax = 64 << 10

// errPiNoRecord means the session holds no message on the active branch.
var errPiNoRecord = fmt.Errorf("%w: the pi session has no answer", domain.ErrNoReply)

// PiReader implements domain.ReplySource for pi. Herdr's pi integration
// reports the pane's session file as agent_session {kind: "path"}, so the
// reader opens exactly that file; it never guesses a file from the working
// directory, where two pi panes would collide. The file must be a regular
// .jsonl file whose first line is a pi session header, so a path to any
// other file yields nothing.
//
// A pi session is a tree: every entry names its parent, and the active
// branch is the chain from the last line back to the root (/tree moves the
// leaf, and the next append hangs off it). The reply is the newest
// assistant message on that chain when it ended the turn (no tool call,
// stop reason stop or length); tool work or a newer prompt means the turn
// is still running and answers domain.ErrReplyPending.
//
// Like CodexReader, the session tuple is used for one lookup and never
// stored or logged, and neither is the path, which names the working
// directory and the session id. A tuple whose digest differs from the
// topic's key means the pane now runs another session: the reader answers
// ErrNoReply rather than post that session's answer into this topic.
type PiReader struct {
	session func(ctx context.Context, paneID string) (domain.SessionTuple, error)
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
}

// NewPiReader wires the reader over Herdr's session lookup.
func NewPiReader(session func(context.Context, string) (domain.SessionTuple, error), log *slog.Logger) *PiReader {
	return newPiReader(session, time.Now, log)
}

// newPiReader takes the clock so tests can pin the reply's age. No home
// seam is needed: the path comes from the tuple.
func newPiReader(session func(context.Context, string) (domain.SessionTuple, error),
	now func() time.Time, log *slog.Logger) *PiReader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &PiReader{session: session, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply returns the newest finished answer of the pane's pi session.
// Every failure is domain.ErrNoReply wrapped with a reason that carries no
// session value and no path; a running turn is domain.ErrReplyPending.
func (r *PiReader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindPi {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, agent.Kind)
	}
	tuple, err := r.session(ctx, agent.PaneID)
	if err != nil {
		return domain.Reply{}, classifySessionError(ctx, err, "session lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if tuple.Agent == kindPi && tuple.Kind == "id" {
		// The integration falls back to the id when pi has no session
		// file (--no-session keeps the session in memory).
		return domain.Reply{}, fmt.Errorf("%w: herdr reports no pi session file for the pane", domain.ErrNoReply)
	}
	if tuple.Agent != kindPi || tuple.Kind != "path" || tuple.Value == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports no pi session for the pane", domain.ErrNoReply)
	}
	digest := tuple.Digest()
	if digest == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports an incomplete session for the pane", domain.ErrNoReply)
	}
	if agent.SessionDigest == "" || digest != agent.SessionDigest {
		return domain.Reply{}, fmt.Errorf("%w: the pane now runs another session", domain.ErrNoReply)
	}
	if err := piCheckPath(tuple.Value); err != nil {
		return domain.Reply{}, err
	}
	// The directory itself may be a link (the user's choice); the open
	// below can never leave it.
	dir, err := os.OpenRoot(filepath.Dir(tuple.Value))
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no pi session directory", domain.ErrNoReply)
	}
	defer dir.Close()
	f, info, err := openPiSession(dir, filepath.Base(tuple.Value))
	if errors.Is(err, fs.ErrNotExist) {
		return domain.Reply{}, fmt.Errorf("%w: the pi session has no file yet", domain.ErrNoReply)
	}
	if err != nil {
		return domain.Reply{}, err
	}
	defer f.Close()
	if err := piCheckHeader(f, info.Size()); err != nil {
		return domain.Reply{}, err
	}
	return r.readSession(ctx, f, info, agent.PaneID)
}

// readSession walks one checked session file and builds the reply.
func (r *PiReader) readSession(ctx context.Context, f *os.File, info os.FileInfo, pane string) (domain.Reply, error) {
	text, meta, scan, err := piLastReplyFrom(f, info.Size(), r.maxScan)
	if errors.Is(err, domain.ErrReplyPending) {
		r.log.Debug("pi reply pending", slog.String("pane", pane), slog.String("reason", err.Error()))
		return domain.Reply{}, err
	}
	if err != nil {
		return domain.Reply{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if scan.readErr != nil {
		r.log.Debug("[FIX] pi turn start not reached, meta partial", slog.String("pane", pane),
			slog.String("err", scan.readErr.Error()))
	}
	written := meta.Ended
	if written.IsZero() {
		written = info.ModTime()
	}
	age := r.now().Sub(written)
	r.log.Debug("pi reply found", slog.String("pane", pane),
		slog.Int("lines", scan.lines), slog.Int64("bytes", scan.bytes), slog.Int("skipped_json", scan.skipped),
		slog.Int("off_branch", scan.offBranch), slog.Int("chars", len(text)), slog.Int64("age_ms", age.Milliseconds()),
		slog.String("model", meta.Model), slog.Int("files", len(meta.Files)), slog.Int("output_tokens", meta.OutputTokens))
	return domain.Reply{Text: text, Source: "pi session", Age: age, Written: written, Meta: meta}, nil
}

// piCheckPath refuses a reported path before it touches the file system:
// it must be absolute, already clean (no "..", no doubled separators), a
// .jsonl file name, and free of NUL bytes.
func piCheckPath(p string) error {
	switch {
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("%w: the pi session path is malformed", domain.ErrNoReply)
	case !filepath.IsAbs(p):
		return fmt.Errorf("%w: the pi session path is not absolute", domain.ErrNoReply)
	case filepath.Clean(p) != p:
		return fmt.Errorf("%w: the pi session path is not clean", domain.ErrNoReply)
	case filepath.Ext(p) != ".jsonl":
		return fmt.Errorf("%w: the pi session path is not a jsonl file", domain.ErrNoReply)
	}
	return nil
}

// openPiSession opens the session file in dir through openRegular, so a
// link or FIFO planted in place of the session is refused rather than
// followed or waited on.
func openPiSession(dir *os.Root, name string) (*os.File, os.FileInfo, error) {
	f, info, err := openRegular(dir, name)
	if err != nil {
		return nil, nil, openFailure("the pi session", err)
	}
	return f, info, nil
}

// piCheckHeader requires the first line to be a pi session header, the
// same test pi itself applies when it loads a file: type "session" and a
// string id. A path to any other file is refused here.
func piCheckHeader(f io.ReaderAt, size int64) error {
	if size == 0 {
		return errPiNoRecord
	}
	n := min(size, piHeaderMax)
	buf := make([]byte, n)
	if _, err := (codexReadAt{f}).ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: the pi session could not be read", domain.ErrNoReply)
	}
	end := bytes.IndexByte(buf, '\n')
	if end < 0 {
		return fmt.Errorf("%w: not a pi session file", domain.ErrNoReply)
	}
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf[:end]), &head); err != nil || head.Type != "session" || head.ID == "" {
		return fmt.Errorf("%w: not a pi session file", domain.ErrNoReply)
	}
	return nil
}

// piHead is the part of every entry the branch walk needs. ParentID is
// null on a root; v1 files have neither id nor parentId.
type piHead struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
}

// piEntry is a message entry decoded in full.
type piEntry struct {
	Message piMessage `json:"message"`
}

// piMessage is the part of an agent message the reader uses.
type piMessage struct {
	Role       string    `json:"role"`
	StopReason string    `json:"stopReason"`
	Model      string    `json:"model"`
	Content    []piBlock `json:"-"`
	Usage      struct {
		Output int `json:"output"`
	} `json:"usage"`
}

// UnmarshalJSON keeps content raw: a user message's content may be a plain
// string, and only assistant content is decoded into blocks.
func (m *piMessage) UnmarshalJSON(b []byte) error {
	type plain piMessage
	var aux struct {
		plain
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*m = piMessage(aux.plain)
	if m.Role != "assistant" || len(aux.Content) == 0 {
		return nil
	}
	return json.Unmarshal(aux.Content, &m.Content)
}

// piBlock is one assistant content block: text, thinking or toolCall.
// Only the path argument of a tool call is decoded.
type piBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Name      string `json:"name"`
	Arguments struct {
		Path string `json:"path"`
	} `json:"arguments"`
}

// piEditTools are pi's tools whose path argument names a file they change.
var piEditTools = map[string]bool{"edit": true, "write": true}

// hasToolCall reports whether the message called a tool: its text is then
// commentary before the work, never the turn's answer.
func (m piMessage) hasToolCall() bool {
	for _, b := range m.Content {
		if b.Type == "toolCall" {
			return true
		}
	}
	return false
}

// text joins the message's text blocks; thinking is never part of it.
func (m piMessage) text() string {
	var parts []string
	for _, b := range m.Content {
		if b.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// editedPathsNewestFirst lists the edit and write paths of the message,
// last call first, to match the newest-first order of the walk.
func (m piMessage) editedPathsNewestFirst() []string {
	var out []string
	for i := len(m.Content) - 1; i >= 0; i-- {
		b := m.Content[i]
		if b.Type == "toolCall" && piEditTools[b.Name] && b.Arguments.Path != "" {
			out = append(out, b.Arguments.Path)
		}
	}
	return out
}

// piScan is scanStats plus the entries skipped because they sit on an
// abandoned branch.
type piScan struct {
	scanStats
	offBranch int
}

// piWalk is the state of one backward walk along the active branch.
type piWalk struct {
	scan     piScan
	leafSeen bool
	linear   bool   // v1 file: no ids, every line is on the branch
	want     string // id of the next entry on the branch; "" once the root is passed
	text     string
	meta     domain.TurnMeta
	files    []string // newest first
	started  bool     // the turn's prompt was met
	atHeader bool     // the session header was met: the file start
}

// onBranch reports whether an entry belongs to the active branch and moves
// the walk to its parent. Parents always precede their children in the
// append-only file, so one reverse pass follows the chain.
func (w *piWalk) onBranch(h piHead) bool {
	if !w.leafSeen {
		w.leafSeen = true
		w.linear = h.ID == ""
	} else if !w.linear && (h.ID == "" || h.ID != w.want) {
		w.scan.offBranch++
		return false
	}
	if !w.linear {
		w.want = ""
		if h.ParentID != nil {
			w.want = *h.ParentID
		}
	}
	return true
}

// visit handles one line, newest first.
func (w *piWalk) visit(line []byte) error {
	w.scan.lines++
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var h piHead
	if err := json.Unmarshal(line, &h); err != nil {
		if !w.leafSeen {
			// The newest entry cannot be read: skipping it would answer
			// from an older point of the session.
			return fmt.Errorf("%w: the newest pi entry is unreadable", domain.ErrNoReply)
		}
		w.scan.skipped++
		return nil
	}
	if h.Type == "session" {
		w.atHeader = true
		return errStop
	}
	// Every line gets the small head decode, because the active branch is
	// followed through every entry's id and parentId; only message entries
	// on it are decoded in full.
	if !w.onBranch(h) || h.Type != "message" {
		return nil
	}
	var e piEntry
	if err := json.Unmarshal(line, &e); err != nil {
		if w.text == "" {
			return fmt.Errorf("%w: the newest pi message is unreadable", domain.ErrNoReply)
		}
		w.scan.skipped++
		return nil
	}
	if w.text == "" {
		return w.decide(h, e.Message)
	}
	return w.absorb(h, e.Message)
}

// decide handles the newest message on the branch: it is the answer, a
// running turn, or a turn that ended without one.
func (w *piWalk) decide(h piHead, m piMessage) error {
	switch m.Role {
	case "user":
		return fmt.Errorf("%w: the pi turn has no answer yet", domain.ErrReplyPending)
	case "toolResult":
		return fmt.Errorf("%w: the pi turn is still running", domain.ErrReplyPending)
	case "assistant":
	default:
		// bashExecution, custom, system: not part of the turn's answer.
		return nil
	}
	if m.hasToolCall() {
		return fmt.Errorf("%w: the pi turn is still running", domain.ErrReplyPending)
	}
	switch m.StopReason {
	case "toolUse", "deferred":
		return fmt.Errorf("%w: the pi turn is still running", domain.ErrReplyPending)
	case "error", "aborted":
		return fmt.Errorf("%w: the pi turn ended without an answer", domain.ErrNoReply)
	case "stop", "length", "":
	default:
		return fmt.Errorf("%w: the pi turn ended with an unknown stop reason", domain.ErrNoReply)
	}
	text := m.text()
	if text == "" {
		return fmt.Errorf("%w: the pi answer has no text", domain.ErrNoReply)
	}
	w.text = text
	w.meta.Ended = parseStamp(h.Timestamp)
	w.meta.Model = m.Model
	w.meta.OutputTokens = m.Usage.Output
	return nil
}

// absorb folds an older message of the answered turn into the meta, and
// stops at the prompt that opened the turn.
func (w *piWalk) absorb(h piHead, m piMessage) error {
	switch m.Role {
	case "user":
		w.meta.Started = parseStamp(h.Timestamp)
		w.started = true
		return errStop
	case "assistant":
		w.meta.OutputTokens += m.Usage.Output
		w.files = append(w.files, m.editedPathsNewestFirst()...)
	}
	return nil
}

// piLastReplyFrom walks a session file back from its end along the active
// branch. The newest message on the branch decides: a finished answer is
// returned, and the walk goes on to the prompt that opened the turn for its
// start time, model tokens and edited files; tool work or a newer prompt
// means the turn is still running (ErrReplyPending). When the budget, the
// file or a read runs out after the answer was found, the answer is
// returned with partial meta.
func piLastReplyFrom(f io.ReaderAt, size, budget int64) (string, domain.TurnMeta, piScan, error) {
	f = codexReadAt{f}
	var w piWalk
	if size == 0 {
		return "", domain.TurnMeta{}, w.scan, errPiNoRecord
	}
	// pi appends whole lines; one without its newline is being written,
	// and walking past it would answer from an older point.
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: the pi session could not be read", domain.ErrNoReply)
	}
	if last[0] != '\n' {
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: the pi session ends mid-record", domain.ErrReplyPending)
	}
	bytesRead, err := walkBack(f, size, budget, w.visit)
	w.scan.bytes = bytesRead
	w.meta.Files = uniqueReversed(w.files)
	switch {
	case w.text != "" && (w.started || (errors.Is(err, errStop) && w.atHeader)):
		return w.text, w.meta, w.scan, nil
	case w.text != "":
		// The prompt is beyond the budget, the file start or a read
		// failure: the answer found is worth more than the missing start.
		if err == nil {
			err = errors.New("turn start not reached")
		}
		w.scan.readErr = err
		return w.text, w.meta, w.scan, nil
	case err != nil && !errors.Is(err, errStop):
		return "", domain.TurnMeta{}, w.scan, err
	case bytesRead >= budget && size > budget:
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: no pi answer within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", domain.TurnMeta{}, w.scan, errPiNoRecord
}
