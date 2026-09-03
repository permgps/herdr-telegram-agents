package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

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
// last-prompt, ...); everything but assistant and user is ignored.
type record struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentPart is one element of message.content when it is a list.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// lastReplyIn walks the transcript backwards, newest line first, and
// returns the first non-empty text part of an assistant record met before
// the operator's last prompt. A prompt is a user record that is not meta
// and carries no tool_result; tool results are user records too, so they
// do not end the scan. Sidechain (subagent) records are skipped.
func lastReplyIn(path string, budget int64) (string, scanStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", scanStats{}, fmt.Errorf("%w: open transcript: %v", domain.ErrNoReply, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", scanStats{}, fmt.Errorf("%w: stat transcript: %v", domain.ErrNoReply, err)
	}
	var stats scanStats
	var found string
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
				return fmt.Errorf("%w: no reply text after the last prompt", domain.ErrNoReply)
			}
		case "assistant":
			if text, ok := lastText(rec.Message.Content); ok {
				found = text
				return errStop
			}
		}
		return nil
	}
	bytesRead, err := walkBack(f, info.Size(), budget, visit)
	stats.bytes = bytesRead
	switch {
	case errors.Is(err, errStop):
		return found, stats, nil
	case err != nil:
		return "", stats, err
	case bytesRead >= budget && info.Size() > budget:
		return "", stats, fmt.Errorf("%w: no prompt within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", stats, fmt.Errorf("%w: transcript has no reply", domain.ErrNoReply)
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
