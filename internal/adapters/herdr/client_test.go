package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func newClient(t *testing.T, s *testkit.NDJSONServer) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := Connect(ctx, s.Path(), testLogger(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClientCallSuccess(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		if string(params) != "{}" {
			return nil, &testkit.APIError{Code: "bad_params", Message: string(params)}
		}
		return map[string]any{"type": "pong", "version": "0.7.5", "protocol": 17}, nil
	})
	c := newClient(t, s)
	pong, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if pong.Version != "0.7.5" || pong.Protocol != 17 {
		t.Fatalf("pong = %+v", pong)
	}
	if reqs := s.Requests(); len(reqs) != 1 || reqs[0].ID != "r1" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestClientAPIError(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.get", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "not_found", Message: "no such agent"}
	})
	c := newClient(t, s)
	err := c.Call(context.Background(), "agent.get", map[string]string{"target": "x"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" || apiErr.Message != "no such agent" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientCallTimeout(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	block := make(chan struct{})
	s.Handle("slow", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		<-block
		return nil, nil
	})
	t.Cleanup(func() { close(block) })
	c := newClient(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Call(ctx, "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestClientConcurrentCalls(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("echo", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(params, &p)
		// Interleave replies by delaying odd requests a little.
		if p.N%2 == 1 {
			time.Sleep(2 * time.Millisecond)
		}
		return map[string]int{"n": p.N}, nil
	})
	c := newClient(t, s)
	const calls = 50
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var out struct {
				N int `json:"n"`
			}
			if err := c.Call(context.Background(), "echo", map[string]int{"n": n}, &out); err != nil {
				errs <- err
				return
			}
			if out.N != n {
				errs <- fmt.Errorf("call %d got %d", n, out.N)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := len(s.Requests()); got != calls {
		t.Fatalf("server saw %d requests", got)
	}
}

func TestClientDisconnect(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	block := make(chan struct{})
	s.Handle("hang", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		<-block
		return nil, nil
	})
	t.Cleanup(func() { close(block) })
	c := newClient(t, s)

	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "hang", nil, nil) }()
	s.WaitRequests("hang", 1, time.Second)
	s.CloseAll()

	select {
	case err := <-errc:
		if !errors.Is(err, domain.ErrDisconnected) {
			t.Fatalf("pending call err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending call did not fail after server closed")
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() not closed")
	}
	if err := c.Call(context.Background(), "ping", nil, nil); !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("call after disconnect err = %v", err)
	}
}

func TestClientCloseUnblocksCallers(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	block := make(chan struct{})
	s.Handle("hang", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		<-block
		return nil, nil
	})
	t.Cleanup(func() { close(block) })
	c := newClient(t, s)
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "hang", nil, nil) }()
	s.WaitRequests("hang", 1, time.Second)
	_ = c.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, domain.ErrDisconnected) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the caller")
	}
}
