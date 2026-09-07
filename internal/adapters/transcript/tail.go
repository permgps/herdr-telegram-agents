package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// blockSize is how much of the file is read per step while walking back.
const blockSize = 64 << 10

// errStop ends a backward walk once the reply has been found.
var errStop = errors.New("reply found")

// scanStats describes one backward scan, for the debug log.
type scanStats struct {
	lines   int
	bytes   int64
	skipped int
}

// record is the slice of a transcript line the reader needs. Claude Code
// writes many record types (assistant, user, system, attachment, mode,
// last-prompt, ...); everything but assistant and user is ignored. One
// API response is written as several assistant records (thinking, text,
// tool_use) that share the RequestID and repeat the same usage.
type record struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Timestamp   string `json:"timestamp"`
	RequestID   string `json:"requestId"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// contentPart is one element of message.content when it is a list. Name
// and Input are set on tool_use parts; only the edited path is decoded.
type contentPart struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"input"`
}

// turnStats is what the backward walk learns about the turn between the
// end of the file and the operator's last prompt. complete is false when
// the walk ended (budget or file start) before it met the prompt, in
// which case started is zero and the counts cover only the newest part.
type turnStats struct {
	model        string
	started      time.Time
	ended        time.Time
	files        []string
	outputTokens int
	seenRequests map[string]bool
	complete     bool
}

// editTools are the Claude Code tools whose input names a file they
// change; Read and Grep are not edits.
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// meta returns the stats as the domain type, files in file order with
// duplicates dropped.
func (t turnStats) meta() domain.TurnMeta {
	return domain.TurnMeta{
		Model:        t.model,
		Started:      t.started,
		Ended:        t.ended,
		Files:        uniqueReversed(t.files),
		OutputTokens: t.outputTokens,
	}
}

// uniqueReversed reverses newest-first paths into file order and keeps
// the first occurrence of each.
func uniqueReversed(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for i := len(paths) - 1; i >= 0; i-- {
		if seen[paths[i]] {
			continue
		}
		seen[paths[i]] = true
		out = append(out, paths[i])
	}
	return out
}

// absorb folds one non-sidechain assistant record into the stats: the
// newest timestamp is the end of the turn, the first model met is the
// model, output tokens count once per request id, and every edited path
// is kept in the order met (newest first).
func (t *turnStats) absorb(rec record) {
	if t.ended.IsZero() {
		t.ended = parseStamp(rec.Timestamp)
	}
	if t.model == "" {
		t.model = rec.Message.Model
	}
	if rec.RequestID == "" {
		t.outputTokens += rec.Message.Usage.OutputTokens
	} else if !t.seenRequests[rec.RequestID] {
		if t.seenRequests == nil {
			t.seenRequests = map[string]bool{}
		}
		t.seenRequests[rec.RequestID] = true
		t.outputTokens += rec.Message.Usage.OutputTokens
	}
	t.files = append(t.files, editedPaths(rec.Message.Content)...)
}

// parseStamp reads a transcript timestamp (RFC 3339, milliseconds); a
// value that does not parse is the zero time.
func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return ts
}

// editedPaths lists the file_path / notebook_path inputs of the edit
// tools in an assistant content list.
func editedPaths(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if p.Type != "tool_use" || !editTools[p.Name] {
			continue
		}
		if p.Input.FilePath != "" {
			out = append(out, p.Input.FilePath)
		}
		if p.Input.NotebookPath != "" {
			out = append(out, p.Input.NotebookPath)
		}
	}
	return out
}

// lastReplyIn walks the transcript backwards, newest line first, and
// returns the first non-empty text part of an assistant record met before
// the operator's last prompt, together with what the records of that turn
// say about it (turnStats). A prompt is a user record that is not meta
// and carries no tool_result; tool results are user records too, so they
// do not end the scan. Sidechain (subagent) records are skipped. The walk
// goes on past the text until the prompt so the stats cover the whole
// turn; when the budget or the file runs out first, a text already found
// is returned with partial stats (complete false) rather than an error.
func lastReplyIn(path string, budget int64) (string, turnStats, scanStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", turnStats{}, scanStats{}, fmt.Errorf("%w: open transcript: %v", domain.ErrNoReply, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", turnStats{}, scanStats{}, fmt.Errorf("%w: stat transcript: %v", domain.ErrNoReply, err)
	}
	var stats scanStats
	var turn turnStats
	var found string
	var haveText bool
	visit := func(line []byte) error {
		stats.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			stats.skipped++
			return nil
		}
		if rec.IsSidechain {
			return nil // subagent traffic, prompts included
		}
		switch rec.Type {
		case "user":
			if isPrompt(rec) {
				turn.started = parseStamp(rec.Timestamp)
				turn.complete = true
				if !haveText {
					return fmt.Errorf("%w: no reply text after the last prompt", domain.ErrNoReply)
				}
				return errStop
			}
		case "assistant":
			turn.absorb(rec)
			if haveText {
				return nil
			}
			if text, ok := lastText(rec.Message.Content); ok {
				found, haveText = text, true
			}
		}
		return nil
	}
	bytesRead, err := walkBack(f, info.Size(), budget, visit)
	stats.bytes = bytesRead
	switch {
	case errors.Is(err, errStop):
		return found, turn, stats, nil
	case err != nil:
		return "", turnStats{}, stats, err
	case haveText:
		return found, turn, stats, nil // the prompt is beyond the budget or the file start
	case bytesRead >= budget && info.Size() > budget:
		return "", turnStats{}, stats, fmt.Errorf("%w: no prompt within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", turnStats{}, stats, fmt.Errorf("%w: transcript has no reply", domain.ErrNoReply)
}

// isPrompt reports whether a user record is something the operator typed:
// not a meta record (local command output, turn companion), not a tool
// result and not an empty record. A non-empty string content is a prompt.
func isPrompt(rec record) bool {
	if rec.IsMeta {
		return false
	}
	raw := bytes.TrimSpace(rec.Message.Content)
	if len(raw) == 0 || bytes.Equal(raw, []byte(`""`)) {
		return false // no message at all: not something the operator typed
	}
	if raw[0] != '[' {
		return true
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return true
	}
	for _, p := range parts {
		if p.Type == "tool_result" {
			return false
		}
	}
	return true
}

// lastText returns the last non-empty text part of an assistant content
// list, trimmed; a plain string content counts as one text part.
func lastText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		s = strings.TrimSpace(s)
		return s, s != ""
	}
	if raw[0] != '[' {
		return "", false
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].Type != "text" {
			continue
		}
		if text := strings.TrimSpace(parts[i].Text); text != "" {
			return text, true
		}
	}
	return "", false
}

// walkBack reads f from its end in blocks and calls visit for every
// complete line, newest first, until visit returns an error, the file is
// exhausted, or budget bytes have been read. It returns the bytes read
// and the error visit stopped with, if any.
func walkBack(f *os.File, size, budget int64, visit func(line []byte) error) (int64, error) {
	pos := size
	var read int64
	var carry []byte // the head of a line that continues in the earlier block
	buf := make([]byte, blockSize)
	for pos > 0 && read < budget {
		n := int64(blockSize)
		if n > pos {
			n = pos
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil {
			return read, fmt.Errorf("%w: read transcript: %v", domain.ErrNoReply, err)
		}
		read += n
		data := append(append([]byte(nil), buf[:n]...), carry...)
		segments := bytes.Split(data, []byte{'\n'})
		first := 0
		if pos > 0 {
			// The first segment is a line cut by the block boundary.
			carry = segments[0]
			first = 1
		} else {
			carry = nil
		}
		for i := len(segments) - 1; i >= first; i-- {
			if err := visit(segments[i]); err != nil {
				return read, err
			}
		}
	}
	return read, nil
}
