package testkit_test

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func dial(t *testing.T, path string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn, bufio.NewScanner(conn)
}

func send(t *testing.T, conn net.Conn, line string) {
	t.Helper()
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readJSON(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	if !sc.Scan() {
		t.Fatalf("no line: %v", sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", sc.Text(), err)
	}
	return m
}

func TestRoundTrip(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "pong", "protocol": 17}, nil
	})
	conn, sc := dial(t, s.Path())
	send(t, conn, `{"id":"r1","method":"ping","params":{}}`)
	got := readJSON(t, sc)
	if got["id"] != "r1" {
		t.Fatalf("id = %v", got["id"])
	}
	if res, _ := got["result"].(map[string]any); res["type"] != "pong" {
		t.Fatalf("result = %v", got["result"])
	}

	send(t, conn, `{"id":"r2","method":"nope","params":{}}`)
	got = readJSON(t, sc)
	if e, _ := got["error"].(map[string]any); e["code"] != "unknown_method" {
		t.Fatalf("error = %v", got["error"])
	}

	reqs := s.Requests()
	if len(reqs) != 2 || reqs[0].Method != "ping" || reqs[1].ID != "r2" || reqs[1].Conn != 1 {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestSubscriptionPushAndCloseOnWrite(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	conn, sc := dial(t, s.Path())
	send(t, conn, `{"id":"s1","method":"events.subscribe","params":{"subscriptions":[{"type":"pane.closed"}]}}`)
	got := readJSON(t, sc)
	if res, _ := got["result"].(map[string]any); res["type"] != "subscription_started" {
		t.Fatalf("subscribe result = %v", got)
	}
	if s.SubscriptionCount() != 1 {
		t.Fatalf("subscriptions = %d", s.SubscriptionCount())
	}

	if n := s.Push("pane_closed", map[string]string{"pane_id": "w1:p1"}); n != 1 {
		t.Fatalf("push reached %d conns", n)
	}
	got = readJSON(t, sc)
	if got["event"] != "pane_closed" {
		t.Fatalf("event = %v", got)
	}
	if data, _ := got["data"].(map[string]any); data["pane_id"] != "w1:p1" {
		t.Fatalf("data = %v", got["data"])
	}

	// A second request on a subscription connection gets it closed.
	send(t, conn, `{"id":"r9","method":"ping","params":{}}`)
	if sc.Scan() {
		t.Fatalf("expected EOF after writing to a subscription, got %q", sc.Text())
	}
	if !s.WaitConns(0, time.Second) {
		t.Fatalf("connection still open: %d", s.ConnCount())
	}
}

func TestCloseAllDropsClients(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	_, sc1 := dial(t, s.Path())
	_, sc2 := dial(t, s.Path())
	if !s.WaitConns(2, time.Second) {
		t.Fatalf("conns = %d", s.ConnCount())
	}
	s.CloseAll()
	if sc1.Scan() || sc2.Scan() {
		t.Fatalf("clients still connected")
	}
	// The listener survives a CloseAll.
	conn, sc := dial(t, s.Path())
	send(t, conn, `{"id":"r1","method":"x","params":{}}`)
	if got := readJSON(t, sc); got["id"] != "r1" {
		t.Fatalf("server not answering after CloseAll: %v", got)
	}
}
