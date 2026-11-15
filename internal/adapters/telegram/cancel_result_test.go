package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type lateResultClient struct {
	started chan struct{}
	body    string
}

func (c *lateResultClient) Do(req *http.Request) (*http.Response, error) {
	close(c.started)
	<-req.Context().Done()
	time.Sleep(20 * time.Millisecond) // The response has completed as cancellation arrives.
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(c.body))}, nil
}

func TestGatewayLateResultAfterCancel(t *testing.T) {
	for _, operation := range []string{"message", "topic"} {
		t.Run(operation, func(t *testing.T) {
			body := `{"ok":true,"result":{"message_id":42,"message_thread_id":42,"name":"test"}}`
			client := &lateResultClient{started: make(chan struct{}), body: body}
			b, err := NewBot("123:test", nil, nil, bot.WithHTTPClient(time.Second, client))
			if err != nil {
				t.Fatal(err)
			}
			q := NewQueue(nil, QueueConfig{MinGap: time.Nanosecond})
			runCtx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); q.Run(runCtx) }()
			defer func() { stop(); <-done }()
			g := NewGateway(b, Config{ChatID: -100}, q, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { <-client.started; cancel() }()
			if operation == "message" {
				_, err = g.Send(ctx, domain.Outgoing{Text: "test"})
			} else {
				_, err = g.CreateTopic(ctx, "test", domain.StatusWorking)
			}
			if err == nil {
				t.Fatal("expected cancellation")
			}
			if err := q.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}
