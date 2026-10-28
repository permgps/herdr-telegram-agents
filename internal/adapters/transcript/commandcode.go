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
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// kindCommandCode is the Herdr agent kind CommandCodeReader understands.
const kindCommandCode = "command-code"

// commandCodeHeaderMax bounds the read of a session file's first line.
const commandCodeHeaderMax = 64 << 10

// errCommandCodeNoRecord means the session holds no message to answer from.
var errCommandCodeNoRecord = fmt.Errorf("%w: the command-code session has no answer", domain.ErrNoReply)

// commandCodeSessionIDPattern matches the session ids Command Code writes;
// anything else never reaches the path.
var commandCodeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// commandCodeDir is Command Code's transcript root, relative to the home.
var commandCodeDir = []string{".commandcode", "projects"}

// CommandCodeReader implements domain.ReplySource for command-code. Herdr's
// Command Code integration reports the pane's session id as agent_session
// {kind: "id"}, and Command Code writes one file per session under
// ~/.commandcode/projects/<slug of the working directory>/, named after that
// id. The reader opens exactly that file and never guesses between sessions;
// two panes in one directory each keep their own answer.
//
// The transcript is Claude Code-shaped: one message entry per API response
// with content blocks (thinking, text, tool_use for the agent; text,
// tool_result for the user), the model and the usage at the top level of the
// entry. The reply is the newest assistant entry that ends in prose; an
// entry that ends in a tool call, or a newest record that is a tool result
// or a new prompt, means the turn is still running and answers
// domain.ErrReplyPending.
//
// The session value and the path are used for that one lookup and never
// stored or logged. A tuple whose digest differs from the topic's key means
// the pane now runs another session: the reader answers ErrNoReply rather
// than post that session's answer into this topic.
type CommandCodeReader struct {
	home    func() (string, error)
	session func(ctx context.Context, paneID string) (domain.SessionTuple, error)
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
}

// NewCommandCodeReader wires the reader over Herdr's session lookup.
func NewCommandCodeReader(session func(context.Context, string) (domain.SessionTuple, error), log *slog.Logger) *CommandCodeReader {
	return newCommandCodeReader(os.UserHomeDir, session, time.Now, log)
}

// newCommandCodeReader takes the home and clock sources so tests can point
// the reader at a temporary directory.
func newCommandCodeReader(home func() (string, error),
	session func(context.Context, string) (domain.SessionTuple, error),
	now func() time.Time, log *slog.Logger) *CommandCodeReader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CommandCodeReader{home: home, session: session, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply returns the newest finished answer of the pane's command-code
// session. Every failure is domain.ErrNoReply wrapped with a reason that
// carries no session value and no path; a running turn is
// domain.ErrReplyPending.
func (r *CommandCodeReader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindCommandCode {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, agent.Kind)
	}
	tuple, err := r.session(ctx, agent.PaneID)
	if err != nil {
		return domain.Reply{}, classifySessionError(ctx, err, "session lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if tuple.Agent != kindCommandCode || tuple.Kind != "id" || tuple.Value == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports no command-code session for the pane", domain.ErrNoReply)
	}
	if !commandCodeSessionIDPattern.MatchString(tuple.Value) {
		return domain.Reply{}, fmt.Errorf("%w: the command-code session id is malformed", domain.ErrNoReply)
	}
	digest := tuple.Digest()
	if digest == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports an incomplete session for the pane", domain.ErrNoReply)
	}
	if agent.SessionDigest == "" || digest != agent.SessionDigest {
		return domain.Reply{}, fmt.Errorf("%w: the pane now runs another session", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: home directory: %v", domain.ErrNoReply, err)
	}
	projects := filepath.Join(append([]string{home}, commandCodeDir...)...)
	root, err := os.OpenRoot(projects)
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no command-code projects directory", domain.ErrNoReply)
	}
	defer root.Close()
	dir, dirsScanned, err := findCommandCodeSession(root, tuple.Value+".jsonl")
	if err != nil {
		return domain.Reply{}, err
	}
	f, info, err := openRegular(root, filepath.Join(dir, tuple.Value+".jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return domain.Reply{}, fmt.Errorf("%w: the command-code session has no file", domain.ErrNoReply)
	}
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: the command-code session %v", domain.ErrNoReply, err)
	}
	defer f.Close()
	if err := commandCodeCheckHeader(f, info.Size(), tuple.Value); err != nil {
		return domain.Reply{}, err
	}
	r.log.Debug("command-code session located", slog.String("pane", agent.PaneID), slog.Int("dirs", dirsScanned))
	return r.readSession(ctx, f, info, agent.PaneID)
}

// readSession walks one checked session file and builds the reply.
func (r *CommandCodeReader) readSession(ctx context.Context, f *os.File, info os.FileInfo, pane string) (domain.Reply, error) {
	text, meta, scan, err := commandCodeLastReplyFrom(f, info.Size(), r.maxScan)
	if errors.Is(err, domain.ErrReplyPending) {
		r.log.Debug("command-code reply pending", slog.String("pane", pane), slog.String("reason", err.Error()))
		return domain.Reply{}, err
	}
	if err != nil {
		return domain.Reply{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if scan.readErr != nil {
		r.log.Debug("[FIX] command-code turn start not reached, meta partial", slog.String("pane", pane),
			slog.String("err", scan.readErr.Error()))
	}
	written := meta.Ended
	if written.IsZero() {
		written = info.ModTime()
	}
	age := r.now().Sub(written)
	r.log.Debug("command-code reply found", slog.String("pane", pane),
		slog.Int("lines", scan.lines), slog.Int64("bytes", scan.bytes), slog.Int("skipped_json", scan.skipped),
		slog.Int("chars", len(text)), slog.Int64("age_ms", age.Milliseconds()),
		slog.String("model", meta.Model), slog.Int("files", len(meta.Files)), slog.Int("output_tokens", meta.OutputTokens))
	return domain.Reply{Text: text, Source: "command-code session", Age: age, Written: written, Meta: meta}, nil
}

// commandCodeScanBudget bounds how many directory entries the lookup may
// read before giving up: a session file that is not found within one
// screen-post's worth of work costs the screen post, not a long walk.
const commandCodeScanBudget = 50_000

// findCommandCodeSession looks for name inside the project directories
// under root, newest first, and returns the directory that holds it. The
// reader never guesses the directory from the working directory: Command
// Code derives that name itself (lower case, dashes at separators and case
// boundaries) and the rule is not published, so the exact session id is the
// only reliable key. Links are not followed: a directory entry that is a
// link does not count as a directory, and the found file is opened through
// root, so the lookup never leaves the projects directory.
func findCommandCodeSession(root *os.Root, name string) (string, int, error) {
	top, err := root.Open(".")
	if err != nil {
		return "", 0, fmt.Errorf("%w: the command-code projects directory could not be read", domain.ErrNoReply)
	}
	dirs, err := top.ReadDir(-1)
	top.Close()
	if err != nil {
		return "", 0, fmt.Errorf("%w: the command-code projects directory could not be read", domain.ErrNoReply)
	}
	read := 0
	scanned := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub, err := root.Open(d.Name())
		if err != nil {
			continue
		}
		entries, err := sub.ReadDir(-1)
		sub.Close()
		if err != nil {
			continue
		}
		scanned++
		for _, e := range entries {
			read++
			if read > commandCodeScanBudget {
				return "", scanned, fmt.Errorf("%w: no command-code session within %d project entries", domain.ErrNoReply, commandCodeScanBudget)
			}
			if e.Name() == name && e.Type().IsRegular() {
				return d.Name(), scanned, nil
			}
		}
	}
	return "", scanned, fmt.Errorf("%w: the command-code session has no file", domain.ErrNoReply)
}

// commandCodeCheckHeader requires the first line to be the session header
// Command Code writes, with the id Herdr reported. A path to any other file
// is refused here.
func commandCodeCheckHeader(f io.ReaderAt, size int64, want string) error {
	if size == 0 {
		return errCommandCodeNoRecord
	}
	rd := codexReadAt{f}
	n := min(size, int64(commandCodeHeaderMax))
	buf := make([]byte, n)
	if _, err := rd.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: the command-code session could not be read", domain.ErrNoReply)
	}
	end := bytes.IndexByte(buf, '\n')
	if end < 0 {
		return fmt.Errorf("%w: not a command-code session file", domain.ErrNoReply)
	}
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf[:end]), &head); err != nil || head.Type != "session" || head.ID != want {
		return fmt.Errorf("%w: not a command-code session file", domain.ErrNoReply)
	}
	return nil
}

// ccHead is the part of every line the walk needs.
type ccHead struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

// ccEntry is a message entry: the model and usage sit at the top level, next
// to the message.
type ccEntry struct {
	Message struct {
		Role    string    `json:"role"`
		Content []ccBlock `json:"content"`
	} `json:"message"`
	Model string `json:"model"`
	Usage struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"usage"`
}

// ccBlock is one content block; only text, tool_use and the edited path are
// decoded.
type ccBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
	// Input carries the target of an edit tool. Command Code names it
	// file_path; older or variant tools used path, so both count.
	Input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	} `json:"input"`
}

// ccEditTools are the Command Code tools whose input names a file they
// change: write_file creates or overwrites, edit_file changes in place.
var ccEditTools = map[string]bool{"edit_file": true, "write_file": true}

// answerText returns the message's text when the message ends in prose. A
// message that ends in a tool call is commentary before the work, never the
// turn's answer; thinking is never part of it.
func (e ccEntry) answerText() string {
	for i := len(e.Message.Content) - 1; i >= 0; i-- {
		b := e.Message.Content[i]
		switch b.Type {
		case "thinking":
			continue
		case "tool_use":
			return ""
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

// prompt reports whether a user entry is something the operator typed: it
// carries a text block and no tool result. Tool results are user entries
// too, so they never end the scan.
func (e ccEntry) prompt() bool {
	if e.Message.Role != "user" {
		return false
	}
	hasText := false
	for _, b := range e.Message.Content {
		switch b.Type {
		case "tool_result":
			return false
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				hasText = true
			}
		}
	}
	return hasText
}

// editedPathsNewestFirst lists the edit tools' target paths, last call
// first, to match the newest-first order of the walk.
func (e ccEntry) editedPathsNewestFirst() []string {
	var out []string
	for i := len(e.Message.Content) - 1; i >= 0; i-- {
		b := e.Message.Content[i]
		if b.Type != "tool_use" || !ccEditTools[b.Name] {
			continue
		}
		if b.Input.FilePath != "" {
			out = append(out, b.Input.FilePath)
		} else if b.Input.Path != "" {
			out = append(out, b.Input.Path)
		}
	}
	return out
}

// ccWalk is the state of one backward walk over a session file.
type ccWalk struct {
	scan   scanStats
	newest bool // the newest message entry was handled
	text   string
	meta   domain.TurnMeta
	files  []string // newest first
}

// visit handles one line, newest first.
func (w *ccWalk) visit(line []byte) error {
	w.scan.lines++
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var h ccHead
	if err := json.Unmarshal(line, &h); err != nil {
		if !w.newest {
			// The newest entry cannot be read: skipping it would answer
			// from an older point of the session.
			return fmt.Errorf("%w: the newest command-code entry is unreadable", domain.ErrNoReply)
		}
		w.scan.skipped++
		return nil
	}
	if h.Type == "session" {
		return errStop
	}
	if h.Type != "message" {
		return nil
	}
	var e ccEntry
	if err := json.Unmarshal(line, &e); err != nil {
		if !w.newest {
			return fmt.Errorf("%w: the newest command-code entry is unreadable", domain.ErrNoReply)
		}
		w.scan.skipped++
		return nil
	}
	if !w.newest {
		w.newest = true
		return w.decide(h, e)
	}
	return w.absorb(h, e)
}

// decide handles the newest message entry: it is the answer or a running
// turn.
func (w *ccWalk) decide(h ccHead, e ccEntry) error {
	switch e.Message.Role {
	case "user":
		if e.prompt() {
			return fmt.Errorf("%w: the command-code turn has no answer yet", domain.ErrReplyPending)
		}
		return fmt.Errorf("%w: the command-code turn is still running", domain.ErrReplyPending)
	case "assistant":
	default:
		// Another role the format may grow: keep walking.
		return nil
	}
	if text := e.answerText(); text == "" {
		return fmt.Errorf("%w: the command-code turn is still running", domain.ErrReplyPending)
	}
	return w.absorbAssistant(h, e)
}

// absorb folds an older entry into the answer's meta and stops at the
// prompt that opened the turn.
func (w *ccWalk) absorb(h ccHead, e ccEntry) error {
	switch e.Message.Role {
	case "user":
		if e.prompt() {
			w.meta.Started = parseStamp(h.Timestamp)
			return errStop
		}
		return nil
	case "assistant":
		return w.absorbAssistant(h, e)
	}
	return nil
}

// absorbAssistant records a message of the turn: the text of the newest
// prose message is the answer, the model is the first met (the newest), and
// the output tokens of every entry are summed once, since Command Code
// writes one entry per API response.
func (w *ccWalk) absorbAssistant(h ccHead, e ccEntry) error {
	if text := e.answerText(); text != "" && w.text == "" {
		w.text = text
		w.meta.Ended = parseStamp(h.Timestamp)
	}
	w.meta.OutputTokens += e.Usage.OutputTokens
	if w.meta.Model == "" {
		w.meta.Model = e.Model
	}
	w.files = append(w.files, e.editedPathsNewestFirst()...)
	return nil
}

// commandCodeLastReplyFrom walks a session file back from its end. The
// newest message entry decides: an assistant entry that ends in prose is
// returned, and the walk goes on to the prompt that opened the turn for its
// start time, model, tokens and edited files; a reply not yet written, tool
// work or a newer prompt means the turn is still running
// (ErrReplyPending). When the budget, the file or a read runs out after the
// answer was found, the answer is returned with partial meta.
func commandCodeLastReplyFrom(f io.ReaderAt, size, budget int64) (string, domain.TurnMeta, scanStats, error) {
	rd := codexReadAt{f}
	var w ccWalk
	if size == 0 {
		return "", domain.TurnMeta{}, w.scan, errCommandCodeNoRecord
	}
	// Command Code appends whole lines; one without its newline is being
	// written, and walking past it would answer from an older point.
	var last [1]byte
	if _, err := rd.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: the command-code session could not be read", domain.ErrNoReply)
	}
	if last[0] != '\n' {
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: the command-code session ends mid-record", domain.ErrReplyPending)
	}
	bytesRead, err := walkBack(rd, size, budget, w.visit)
	w.scan.bytes = bytesRead
	w.meta.Files = uniqueReversed(w.files)
	switch {
	case w.text != "":
		if err != nil && !errors.Is(err, errStop) {
			w.scan.readErr = err
		}
		return w.text, w.meta, w.scan, nil
	case errors.Is(err, errStop):
		return "", domain.TurnMeta{}, w.scan, errCommandCodeNoRecord
	case err != nil:
		return "", domain.TurnMeta{}, w.scan, err
	case bytesRead >= budget && size > budget:
		return "", domain.TurnMeta{}, w.scan, fmt.Errorf("%w: no command-code answer within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", domain.TurnMeta{}, w.scan, errCommandCodeNoRecord
}
