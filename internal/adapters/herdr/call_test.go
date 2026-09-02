package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestCallSuccessAndPing(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("ping", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		if string(params) != "{}" {
			return nil, &testkit.APIError{Code: "bad_params", Message: string(params)}
		}
		return map[string]any{"type": "pong", "version": "0.7.5", "protocol": 17}, nil
	})
	pong, err := ping(context.Background(), dial, s.Path(), testLogger(t))
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if pong.Version != "0.7.5" || pong.Protocol != 17 {
		t.Fatalf("pong = %+v", pong)
	}
	if reqs := s.Requests(); len(reqs) != 1 || !strings.HasPrefix(reqs[0].ID, "r") {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestCallAPIError(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("agent.get", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return nil, &testkit.APIError{Code: "not_found", Message: "no such agent"}
	})
	err := call(context.Background(), dial, s.Path(), testLogger(t), "agent.get", map[string]string{"target": "x"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" || apiErr.Message != "no such agent" {
		t.Fatalf("err = %v", err)
	}
}

func TestCallTimeout(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	block := make(chan struct{})
	s.Handle("slow", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		<-block
		return nil, nil
	})
	t.Cleanup(func() { close(block) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := call(ctx, dial, s.Path(), testLogger(t), "slow", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestCallConcurrent(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	s.Handle("echo", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(params, &p)
		if p.N%2 == 1 {
			time.Sleep(2 * time.Millisecond)
		}
		return map[string]int{"n": p.N}, nil
	})
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
			if err := call(context.Background(), dial, s.Path(), testLogger(t), "echo", map[string]int{"n": n}, &out); err != nil {
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

func TestCallServerClosesBeforeReply(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	block := make(chan struct{})
	s.Handle("hang", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		<-block
		return nil, nil
	})
	t.Cleanup(func() { close(block) })

	errc := make(chan error, 1)
	go func() { errc <- call(context.Background(), dial, s.Path(), testLogger(t), "hang", nil, nil) }()
	s.WaitRequests("hang", 1, time.Second)
	s.CloseAll()

	select {
	case err := <-errc:
		if !errors.Is(err, domain.ErrDisconnected) {
			t.Fatalf("err = %v, want ErrDisconnected", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not fail after server closed")
	}
}

func TestCallDialFailure(t *testing.T) {
	err := call(context.Background(), dial, "/nonexistent/herdr.sock", testLogger(t), "ping", nil, nil)
	if !errors.Is(err, errDial) {
		t.Fatalf("err = %v, want errDial", err)
	}
}

func TestCallLargeReply(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	big := strings.Repeat("x", 300<<10)
	s.Handle("agent.read", func(id string, params json.RawMessage) (any, *testkit.APIError) {
		return map[string]any{"type": "pane_read", "read": map[string]any{"text": big}}, nil
	})
	var res paneReadResult
	if err := call(context.Background(), dial, s.Path(), testLogger(t), "agent.read", nil, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Read.Text) != len(big) {
		t.Fatalf("text len = %d, want %d", len(res.Read.Text), len(big))
	}
}
