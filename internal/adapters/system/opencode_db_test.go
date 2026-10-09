package system

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// sqliteRow is one row of "sqlite3 -json" output.
type sqliteRow struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// sqliteJSON renders rows the way sqlite3 -json does.
func sqliteJSON(t *testing.T, rows ...sqliteRow) []byte {
	t.Helper()
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dbExport builds an exporter whose database shortcut returns out and whose
// CLI stand-in prints sentinel, so a surprise fallback is visible.
func dbExport(t *testing.T, out []byte, err error) *OpenCodeExporter {
	t.Helper()
	e := NewOpenCodeExporter("", nil)
	e.dbPath = func() (string, error) { return "/nonexistent/opencode.db", nil }
	e.sqlite = func(ctx context.Context, dbPath, query string) ([]byte, error) {
		if !strings.Contains(query, "session_message") || !strings.Contains(query, "type='user'") {
			t.Fatalf("query = %q", query)
		}
		return out, err
	}
	e.bin = fakeOpenCode(t, `echo '{"sentinel":true}'`)
	return e
}

func TestOpenCodeExporterReadsDatabaseFirst(t *testing.T) {
	rows := sqliteJSON(t,
		sqliteRow{"user", `{"time":{"created":1000},"text":"prompt"}`},
		sqliteRow{"assistant", `{
			"model":{"id":"grok-4.7"},
			"time":{"created":1001,"completed":1002},
			"tokens":{"output":7},
			"content":[
				{"type":"reasoning","text":"secret reasoning","providerState":{"blob":"AAA"}},
				{"type":"text","text":"Hello operator"},
				{"type":"tool","name":"edit","state":{"status":"completed","input":{"path":"/tmp/a.go"}}},
				{"type":"step-start"}
			]}`},
	)
	e := dbExport(t, rows, nil)
	out, err := e.Export(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Export err = %v", err)
	}
	if strings.Contains(string(out), "reasoning") || strings.Contains(string(out), "providerState") || strings.Contains(string(out), "step-start") {
		t.Fatalf("bookkeeping leaked into the rebuilt document: %s", out)
	}
	var doc struct {
		Info     struct{ ID string } `json:"info"`
		Messages []struct {
			Type    string `json:"type"`
			Time    struct{ Created, Completed int64 }
			Model   struct{ ID string }
			Tokens  struct{ Output int }
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Tool  string `json:"tool"`
				State struct {
					Status string `json:"status"`
					Input  struct {
						FilePath string `json:"filePath"`
					} `json:"input"`
				} `json:"state"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rebuilt JSON invalid: %v (%s)", err, out)
	}
	if doc.Info.ID != "ses_test" || len(doc.Messages) != 2 {
		t.Fatalf("info=%q messages=%d", doc.Info.ID, len(doc.Messages))
	}
	if doc.Messages[0].Type != "user" || doc.Messages[0].Time.Created != 1000 {
		t.Fatalf("user message = %+v", doc.Messages[0])
	}
	m := doc.Messages[1]
	if m.Type != "assistant" || m.Time.Created != 1001 || m.Time.Completed != 1002 ||
		m.Model.ID != "grok-4.7" || m.Tokens.Output != 7 || len(m.Content) != 2 {
		t.Fatalf("assistant message = %+v", m)
	}
	if m.Content[0].Type != "text" || m.Content[0].Text != "Hello operator" {
		t.Fatalf("text part = %+v", m.Content[0])
	}
	tool := m.Content[1]
	if tool.Type != "tool" || tool.Tool != "edit" || tool.State.Status != "completed" || tool.State.Input.FilePath != "/tmp/a.go" {
		t.Fatalf("tool part = %+v", tool)
	}
}

func TestOpenCodeExporterUsesFilePathOverPath(t *testing.T) {
	rows := sqliteJSON(t,
		sqliteRow{"user", `{"time":{"created":1}}`},
		sqliteRow{"assistant", `{"time":{"created":2},"content":[
		{"type":"text","text":"ok"},
		{"type":"tool","name":"write","state":{"status":"completed","input":{"filePath":"/export/style.go","path":"/db/style.go"}}}
	]}`})
	e := dbExport(t, rows, nil)
	out, err := e.Export(context.Background(), "ses_test")
	if err != nil {
		t.Fatalf("Export err = %v", err)
	}
	if !strings.Contains(string(out), `"filePath":"/export/style.go"`) {
		t.Fatalf("filePath not honoured: %s", out)
	}
}

func TestOpenCodeExporterFallsBackWhenDatabaseEmpty(t *testing.T) {
	for name, sqliteErr := range map[string]error{"no rows": nil, "failed": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			e := dbExport(t, []byte("[]"), sqliteErr)
			out, err := e.Export(context.Background(), "ses_test")
			if err != nil || strings.TrimSpace(string(out)) != `{"sentinel":true}` {
				t.Fatalf("fallback = %q, %v", out, err)
			}
		})
	}
}

func TestOpenCodeExporterRejectsDatabaseUnsafeSessionIDs(t *testing.T) {
	e := NewOpenCodeExporter("", nil)
	e.dbPath = func() (string, error) { return "/nonexistent/opencode.db", nil }
	e.sqlite = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("database must not be queried with an unsafe session id")
		return nil, nil
	}
	e.bin = fakeOpenCode(t, `echo '{"sentinel":true}'`)
	out, err := e.Export(context.Background(), "ses_x'; DROP TABLE session_message;--")
	if err != nil || strings.TrimSpace(string(out)) != `{"sentinel":true}` {
		t.Fatalf("Export = %q, %v", out, err)
	}
}

func TestOpenCodeExporterDatabaseResultCapFallsBack(t *testing.T) {
	huge := strings.Repeat("x", openCodeDBMaxResult+1)
	rows := sqliteJSON(t,
		sqliteRow{"user", `{"time":{"created":1}}`},
		sqliteRow{"assistant", `{"time":{"created":2},"content":[{"type":"text","text":"` + huge + `"}]}`})
	e := dbExport(t, rows, nil)
	out, err := e.Export(context.Background(), "ses_test")
	if err != nil || strings.TrimSpace(string(out)) != `{"sentinel":true}` {
		t.Fatalf("fallback = %q, %v", out, err)
	}
}

func TestOpenCodeExporterFallsBackOnForeignSchema(t *testing.T) {
	for name, rows := range map[string]string{
		"unparsable row":  `[{"type":"assistant","data":"not json"}]`,
		"no user prompt":  `[{"type":"synthetic","data":"{}"}]`,
		"missing columns": `[{"data":"{\"time\":{\"created\":1}}"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			e := dbExport(t, []byte(rows), nil)
			out, err := e.Export(context.Background(), "ses_test")
			if err != nil || strings.TrimSpace(string(out)) != `{"sentinel":true}` {
				t.Fatalf("fallback = %q, %v", out, err)
			}
		})
	}
}

// TestOpenCodeExporterRealDatabase reads a real session when asked:
//
//	OPENCODE_TEST_SESSION=ses_... go test ./internal/adapters/system/ -run RealDatabase -v
//
// It proves the window read answers with a small, well-formed document on
// the operator's own store, which is what the reviewer needs when a long
// session regresses to the 12-line screen.
func TestOpenCodeExporterRealDatabase(t *testing.T) {
	sessionID := os.Getenv("OPENCODE_TEST_SESSION")
	if sessionID == "" {
		t.Skip("set OPENCODE_TEST_SESSION to a real session id to run against the database")
	}
	e := NewOpenCodeExporter("", nil)
	out, err := e.Export(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Export(%s) err = %v", sessionID, err)
	}
	if len(out) > 1<<20 {
		t.Fatalf("rebuilt document is %d bytes", len(out))
	}
	var doc struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || doc.Info.ID != sessionID {
		t.Fatalf("rebuilt document info = %+v, err = %v", doc.Info, err)
	}
	if strings.Contains(string(out), "providerState") {
		t.Fatalf("provider blobs leaked into the document")
	}
	if !strings.Contains(string(out), `"type":"text"`) {
		t.Fatalf("rebuilt document carries no text part")
	}
}
