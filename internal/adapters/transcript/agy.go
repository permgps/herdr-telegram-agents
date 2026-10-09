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

// kindAgy is the Herdr agent kind AgyReader understands (Antigravity CLI).
const kindAgy = "agy"

// agyHomeDir is where Antigravity keeps one directory per conversation,
// relative to the user's home. A variable like codexHomeDir; environment
// overrides are not honoured (see scripts/check-imports.sh).
var agyHomeDir = filepath.Join(".gemini", "antigravity-cli", "brain")

// agyLogsDir holds a conversation's transcripts inside its directory.
var agyLogsDir = filepath.Join(".system_generated", "logs")

// agyTranscripts are the transcript variants, read in this order: the full
// one only when the first is missing or holds no record the reader knows.
var agyTranscripts = []struct{ name, label string }{
	{"transcript.jsonl", "transcript"},
	{"transcript_full.jsonl", "transcript_full"},
}

// agyConversationID is the only shape of conversation id used in a path:
// a UUID. Anything else, a separator or ".." included, is refused before it
// reaches the file system.
var agyConversationID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Markers a transcript line must contain before it is decoded: tool output
// makes single lines very long, and only these records decide the answer.
var (
	agyMarkPlanner = []byte(`"PLANNER_RESPONSE"`)
	agyMarkGeneric = []byte(`"GENERIC"`)
	agyMarkInput   = []byte(`"USER_INPUT"`)
)

// errAgyNoRecord means a transcript variant holds no record the reader
// knows, so the next variant is worth a try.
var errAgyNoRecord = fmt.Errorf("%w: the agy transcript has no answer", domain.ErrNoReply)

// AgyReader implements domain.ReplySource for Antigravity. Herdr reports
// the conversation id as the pane's agent_session, and the CLI keeps a JSONL
// transcript per conversation under
// ~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/, so the
// reader opens exactly that file. The reply is the newest model answer
// (a PLANNER_RESPONSE with text and no tool calls); a turn whose newest
// record is tool work or a new prompt is still running and answers
// domain.ErrReplyPending.
//
// Like CodexReader, the session tuple is used for one lookup and never
// stored or logged, and neither is the transcript path, which contains the
// conversation id. A tuple whose digest differs from the topic's key means
// the pane now runs another conversation: the reader answers ErrNoReply
// rather than post that conversation's answer into this topic.
type AgyReader struct {
	session func(ctx context.Context, paneID string) (domain.SessionTuple, error)
	home    func() (string, error)
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
}

// NewAgyReader wires the reader over Herdr's session lookup and the current
// user's home directory.
func NewAgyReader(session func(context.Context, string) (domain.SessionTuple, error), log *slog.Logger) *AgyReader {
	return newAgyReader(session, os.UserHomeDir, time.Now, log)
}

// newAgyReader takes the home and clock sources so tests can point the
// reader at a temporary directory.
func newAgyReader(session func(context.Context, string) (domain.SessionTuple, error),
	home func() (string, error), now func() time.Time, log *slog.Logger) *AgyReader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AgyReader{session: session, home: home, now: now, log: log, maxScan: defaultMaxScan}
}

// LastReply returns the newest model answer of the pane's Antigravity
// conversation. Every failure is domain.ErrNoReply wrapped with a reason
// that carries no session value and no path; a running turn is
// domain.ErrReplyPending.
func (r *AgyReader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if agent.Kind != kindAgy {
		return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, agent.Kind)
	}
	tuple, err := r.session(ctx, agent.PaneID)
	if err != nil {
		return domain.Reply{}, classifySessionError(ctx, err, "session lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if tuple.Agent != kindAgy || tuple.Kind != "id" || tuple.Value == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports no agy conversation id for the pane", domain.ErrNoReply)
	}
	digest := tuple.Digest()
	if digest == "" {
		return domain.Reply{}, fmt.Errorf("%w: herdr reports an incomplete session for the pane", domain.ErrNoReply)
	}
	if agent.SessionDigest == "" || digest != agent.SessionDigest {
		return domain.Reply{}, fmt.Errorf("%w: the pane now runs another session", domain.ErrNoReply)
	}
	if !agyConversationID.MatchString(tuple.Value) {
		return domain.Reply{}, fmt.Errorf("%w: the agy conversation id is not a uuid", domain.ErrNoReply)
	}
	home, err := r.home()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no home directory", domain.ErrNoReply)
	}
	// The brain directory itself may be a link (the user's choice); the
	// lookup below can never leave it.
	brain, err := os.OpenRoot(filepath.Join(home, agyHomeDir))
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: no agy conversation directory", domain.ErrNoReply)
	}
	defer brain.Close()
	for _, variant := range agyTranscripts {
		rel := filepath.Join(tuple.Value, agyLogsDir, variant.name)
		reply, err := r.readTranscript(ctx, brain, rel, variant.label, agent.PaneID)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errAgyNoRecord) {
			continue
		}
		return reply, err
	}
	return domain.Reply{}, fmt.Errorf("%w: no agy transcript for the conversation", domain.ErrNoReply)
}

// readTranscript reads one transcript variant. A missing file is returned
// as fs.ErrNotExist and an answerless one as errAgyNoRecord, so the caller
// can try the next variant.
func (r *AgyReader) readTranscript(ctx context.Context, root *os.Root, rel, label, pane string) (domain.Reply, error) {
	f, info, err := openAgyTranscript(root, rel)
	if err != nil {
		return domain.Reply{}, err
	}
	defer f.Close()
	text, meta, stats, err := agyLastReplyFrom(f, info.Size(), r.maxScan)
	if errors.Is(err, domain.ErrReplyPending) {
		r.log.Debug("agy reply pending", slog.String("pane", pane), slog.String("file", label),
			slog.String("reason", err.Error()))
		return domain.Reply{}, err
	}
	if err != nil {
		return domain.Reply{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	if stats.readErr != nil {
		r.log.Debug("[FIX] agy turn start not reached, meta partial", slog.String("pane", pane),
			slog.String("err", stats.readErr.Error()))
	}
	written := meta.Ended
	if written.IsZero() {
		written = info.ModTime()
	}
	age := r.now().Sub(written)
	r.log.Debug("agy reply found", slog.String("pane", pane), slog.String("file", label),
		slog.Int("lines", stats.lines), slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped),
		slog.Int("chars", len(text)), slog.Int64("age_ms", age.Milliseconds()))
	return domain.Reply{Text: text, Source: "agy transcript", Age: age, Written: written, Meta: meta}, nil
}

// openAgyTranscript opens a transcript below root through openRegular, so
// a link or FIFO planted in place of the transcript is refused rather than
// followed or waited on.
func openAgyTranscript(root *os.Root, rel string) (*os.File, os.FileInfo, error) {
	f, info, err := openRegular(root, rel)
	if err != nil {
		return nil, nil, openFailure("the agy transcript", err)
	}
	return f, info, nil
}

// agyRecord is the part of one transcript line the reader uses.
type agyRecord struct {
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Content   string          `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
	CreatedAt string          `json:"created_at"`
}

// hasToolCalls reports whether a planner step called tools: such a step's
// text is commentary before the work, never the turn's answer.
func (r agyRecord) hasToolCalls() bool {
	raw := bytes.TrimSpace(r.ToolCalls)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte("[]"))
}

// agyLastReplyFrom walks a transcript back from its end. The newest record
// it knows decides: a model answer is returned, and the walk goes on to the
// prompt that opened the turn for its start time; tool work, a tool call or
// a newer prompt means the turn is still running (ErrReplyPending). When
// the budget or the file runs out after the answer was found, the answer
// is returned with partial meta. Model and edited files are not recorded
// in the transcript and stay empty.
func agyLastReplyFrom(f io.ReaderAt, size, budget int64) (string, domain.TurnMeta, scanStats, error) {
	f = codexReadAt{f}
	var stats scanStats
	var meta domain.TurnMeta
	if size == 0 {
		return "", meta, stats, errAgyNoRecord
	}
	// A record Antigravity is still writing has no newline yet: walking
	// past it would answer with an older step.
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
		return "", meta, stats, fmt.Errorf("%w: the agy transcript could not be read", domain.ErrNoReply)
	}
	if last[0] != '\n' {
		return "", meta, stats, fmt.Errorf("%w: the agy transcript ends mid-record", domain.ErrReplyPending)
	}
	var text string
	visit := func(line []byte) error {
		stats.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil
		}
		if !bytes.Contains(line, agyMarkPlanner) && !bytes.Contains(line, agyMarkGeneric) && !bytes.Contains(line, agyMarkInput) {
			return nil
		}
		var rec agyRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if text == "" {
				// The newest record cannot be read: skipping it would answer
				// with an older step.
				return fmt.Errorf("%w: the newest agy record is unreadable", domain.ErrNoReply)
			}
			stats.skipped++
			return nil
		}
		if text != "" {
			if rec.Type == "USER_INPUT" {
				meta.Started = parseStamp(rec.CreatedAt)
				return errStop
			}
			return nil
		}
		switch rec.Type {
		case "USER_INPUT":
			return fmt.Errorf("%w: the agy turn has no answer yet", domain.ErrReplyPending)
		case "GENERIC":
			return fmt.Errorf("%w: the agy turn is still running", domain.ErrReplyPending)
		case "PLANNER_RESPONSE":
			if rec.hasToolCalls() {
				return fmt.Errorf("%w: the agy turn is still running", domain.ErrReplyPending)
			}
			if rec.Source != "MODEL" || strings.TrimSpace(rec.Content) == "" {
				return nil
			}
			text = strings.TrimSpace(rec.Content)
			meta.Ended = parseStamp(rec.CreatedAt)
		}
		return nil
	}
	bytesRead, err := walkBack(f, size, budget, visit)
	stats.bytes = bytesRead
	switch {
	case errors.Is(err, errStop):
		return text, meta, stats, nil
	case text != "":
		// The prompt is beyond the budget, the file start or a read
		// failure: the answer found is worth more than the missing start.
		stats.readErr = err
		return text, meta, stats, nil
	case err != nil:
		return "", domain.TurnMeta{}, stats, err
	case bytesRead >= budget && size > budget:
		return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: no agy record within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", domain.TurnMeta{}, stats, errAgyNoRecord
}
