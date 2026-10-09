package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// openCodeDBTimeout bounds the database read. A window query answers in
	// tens of milliseconds; this is only a safety net.
	openCodeDBTimeout = 5 * time.Second
	// openCodeDBMaxOutput caps the raw rows sqlite3 may print. The window is
	// tens of KB to a few MB in normal use.
	openCodeDBMaxOutput = 8 << 20
	// openCodeDBMaxResult caps the rebuilt document handed to the reader.
	openCodeDBMaxResult = 4 << 20
	// openCodeSQLiteBin is the CLI used to read opencode's store.
	openCodeSQLiteBin = "sqlite3"
)

// openCodeSessionIDPattern matches the session ids opencode uses; anything
// else never reaches the query string.
var openCodeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// openCodeDBQuery returns the session's rows from the last user message on.
// The reader only needs the newest turn, and the whole-session export can be
// hundreds of MB on long sessions while this window stays small.
const openCodeDBQuery = `SELECT type, data FROM session_message` +
	` WHERE session_id='%s'` +
	` AND seq >= (SELECT MAX(seq) FROM session_message WHERE session_id='%s' AND type='user')` +
	` ORDER BY seq`

// exportFromDB rebuilds the reader's document from opencode's own SQLite
// store. The CLI export serializes the whole session: hundreds of MB on
// long sessions, which no fixed cap can cover, so the done post fell back
// to the 12-line screen. The window since the last user message answers in
// milliseconds and carries only the current turn.
func (e *OpenCodeExporter) exportFromDB(ctx context.Context, sessionID string) ([]byte, error) {
	if e.sqlite == nil || e.dbPath == nil {
		return nil, errors.New("database reader disabled")
	}
	if !openCodeSessionIDPattern.MatchString(sessionID) {
		return nil, errors.New("session id not usable in the database")
	}
	dbPath, err := e.dbPath()
	if err != nil {
		return nil, err
	}
	dbCtx, cancel := context.WithTimeout(ctx, openCodeDBTimeout)
	defer cancel()
	start := time.Now()
	raw, err := e.sqlite(dbCtx, dbPath, fmt.Sprintf(openCodeDBQuery, sessionID, sessionID))
	if err != nil {
		return nil, err
	}
	var rows []openCodeDBRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.New("invalid sqlite output")
	}
	if len(rows) == 0 {
		// The session lives in opencode's legacy store (or does not exist);
		// the CLI export still knows how to read it.
		return nil, errors.New("no rows for the session")
	}
	doc, err := rebuildOpenCodeDBDoc(sessionID, rows)
	if err != nil {
		return nil, err
	}
	if len(doc) > openCodeDBMaxResult {
		return nil, fmt.Errorf("rebuilt document over %d bytes", openCodeDBMaxResult)
	}
	e.log.Debug("opencode read", slog.String("source", "db"), slog.Int64("dur_ms", time.Since(start).Milliseconds()),
		slog.Int("rows", len(rows)), slog.Int("bytes", len(raw)), slog.Int("result_bytes", len(doc)))
	return doc, nil
}

// defaultOpenCodeDBPath returns opencode's SQLite database when it exists.
// opencode keeps its data under $XDG_DATA_HOME (default ~/.local/share).
func defaultOpenCodeDBPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("no home directory")
		}
		dir = filepath.Join(home, ".local", "share")
	}
	path := filepath.Join(dir, "opencode", "opencode.db")
	if _, err := os.Stat(path); err != nil {
		return "", errors.New("database not found")
	}
	return path, nil
}

// runOpenCodeSQLite runs the read-only query through the sqlite3 CLI. A
// missing binary or a schema from another opencode generation is an error
// and the CLI export answers instead.
func runOpenCodeSQLite(ctx context.Context, dbPath, query string) ([]byte, error) {
	bin, err := exec.LookPath(openCodeSQLiteBin)
	if err != nil {
		return nil, errors.New("sqlite3 not on PATH")
	}
	// The database is opened through a file: URI in read-only mode and with
	// an empty init file, so neither a relative path nor ~/.sqliterc can
	// take over the CLI.
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	cmd := command(ctx, bin, "-json", "-init", os.DevNull, "-cmd", ".timeout 4000", uri, query)
	cmd.WaitDelay = openCodeWaitDelay
	cmd.Stderr = io.Discard
	stdout := &limitedWriter{max: openCodeDBMaxOutput}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sqlite3: %w", err)
	}
	if stdout.capped {
		return nil, fmt.Errorf("sqlite3: output over %d bytes", openCodeDBMaxOutput)
	}
	return stdout.buf.Bytes(), nil
}

// openCodeDBRow is one session_message row as sqlite3 -json prints it: the
// row's type and its JSON payload as a string.
type openCodeDBRow struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// openCodeDBMessage is the part of a row's payload the rebuild needs.
type openCodeDBMessage struct {
	Model struct {
		ID string `json:"id"`
	} `json:"model"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Tokens struct {
		Output int `json:"output"`
	} `json:"tokens"`
	Content []openCodeDBPart `json:"content"`
}

// openCodeDBPart is one content item. The store names tools and file inputs
// differently from the CLI export (name/path vs tool/state.input.filePath).
type openCodeDBPart struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	State struct {
		Status string `json:"status"`
		Input  struct {
			FilePath string `json:"filePath"`
			Path     string `json:"path"`
		} `json:"input"`
	} `json:"state"`
}

// rebuildOpenCodeDBDoc converts the store's rows into the record shape
// "opencode session export" produces, keeping only what the reader uses:
// prose, tool names and status, and the turn's model and tokens. The store
// carries provider blobs and tool payloads this reader never reads, so the
// rebuilt document is a fraction of the window's raw size.
func rebuildOpenCodeDBDoc(sessionID string, rows []openCodeDBRow) ([]byte, error) {
	doc := openCodeRebuiltDoc{Info: openCodeRebuiltInfo{ID: sessionID}}
	prompts := 0
	for _, row := range rows {
		switch row.Type {
		case "user":
			prompts++
		case "assistant":
		default:
			continue // idle, compaction, synthetic, system: bookkeeping
		}
		msg, err := rebuildOpenCodeDBMessage(row.Type, row.Data)
		if err != nil {
			return nil, err
		}
		doc.Messages = append(doc.Messages, msg)
	}
	if prompts == 0 {
		// A store whose shape this build does not know (a future schema):
		// fail closed and let the CLI export answer instead.
		return nil, errors.New("no user prompt in the database window")
	}
	return json.Marshal(doc)
}

func rebuildOpenCodeDBMessage(role, data string) (openCodeRebuiltMessage, error) {
	var raw openCodeDBMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return openCodeRebuiltMessage{}, errors.New("invalid message data")
	}
	msg := openCodeRebuiltMessage{
		Type: role,
		Time: openCodeRebuiltTime{Created: raw.Time.Created, Completed: raw.Time.Completed},
	}
	if role == "user" {
		if raw.Time.Created == 0 {
			// A payload this build does not know (renamed fields): fail
			// closed so the export can answer.
			return openCodeRebuiltMessage{}, errors.New("user row without a creation time")
		}
		return msg, nil
	}
	if raw.Time.Created == 0 || len(raw.Content) == 0 {
		// Unmarshal accepts null, {} and renamed fields with zero values;
		// require the shape the rebuild needs before accepting the row.
		return openCodeRebuiltMessage{}, errors.New("assistant row without a creation time or content")
	}
	msg.Model = &openCodeRebuiltModel{ID: raw.Model.ID}
	msg.Tokens = &openCodeRebuiltTokens{Output: raw.Tokens.Output}
	for _, part := range raw.Content {
		switch part.Type {
		case "text":
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			msg.Content = append(msg.Content, openCodeRebuiltPart{Type: "text", Text: part.Text})
		case "tool":
			file := part.State.Input.FilePath
			if file == "" {
				file = part.State.Input.Path
			}
			msg.Content = append(msg.Content, openCodeRebuiltPart{
				Type: "tool",
				Tool: part.Name,
				State: &openCodeRebuiltState{
					Status: part.State.Status,
					Input:  openCodeRebuiltInput{FilePath: file},
				},
			})
		}
	}
	return msg, nil
}

type openCodeRebuiltDoc struct {
	Info     openCodeRebuiltInfo      `json:"info"`
	Messages []openCodeRebuiltMessage `json:"messages"`
}

type openCodeRebuiltInfo struct {
	ID string `json:"id"`
}

type openCodeRebuiltMessage struct {
	Type    string                 `json:"type"`
	Model   *openCodeRebuiltModel  `json:"model,omitempty"`
	Time    openCodeRebuiltTime    `json:"time"`
	Tokens  *openCodeRebuiltTokens `json:"tokens,omitempty"`
	Content []openCodeRebuiltPart  `json:"content,omitempty"`
}

type openCodeRebuiltModel struct {
	ID string `json:"id"`
}

type openCodeRebuiltTime struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed,omitempty"`
}

type openCodeRebuiltTokens struct {
	Output int `json:"output"`
}

type openCodeRebuiltPart struct {
	Type  string                `json:"type"`
	Text  string                `json:"text,omitempty"`
	Tool  string                `json:"tool,omitempty"`
	State *openCodeRebuiltState `json:"state,omitempty"`
}

type openCodeRebuiltState struct {
	Status string               `json:"status"`
	Input  openCodeRebuiltInput `json:"input"`
}

type openCodeRebuiltInput struct {
	FilePath string `json:"filePath,omitempty"`
}
