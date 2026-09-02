package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Client is one request/response connection to the Herdr server.
//
// Calls may run concurrently: writes are serialised by a mutex and replies
// are matched to callers by id. When the read loop ends, every pending call
// fails with domain.ErrDisconnected and Done() is closed so the owner can
// reconnect. A Client is never reused after that.
type Client struct {
	conn net.Conn
	log  *slog.Logger

	writeMu sync.Mutex
	seq     atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan response

	done      chan struct{}
	closeOnce sync.Once
	readErr   error
}

// Pong is the result of a ping request.
type Pong struct {
	Version  string
	Protocol int
}

// Connect dials the Herdr server at path and starts the read loop.
func Connect(ctx context.Context, path string, log *slog.Logger) (*Client, error) {
	return connect(ctx, dial, path, log)
}

func connect(ctx context.Context, dial dialFunc, path string, log *slog.Logger) (*Client, error) {
	conn, err := dial(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("herdr dial %s: %w", path, err)
	}
	c := &Client{
		conn:    conn,
		log:     log,
		pending: map[string]chan response{},
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Done is closed once the connection is unusable, whether the server went
// away or Close was called.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Close drops the connection. Pending calls fail with ErrDisconnected.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.conn.Close()
	})
	return err
}

// Call sends one request and decodes the result into out (which may be
// nil). A nil params serialises as {} because Herdr requires the field.
// Server-side failures come back as *APIError.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if params == nil {
		params = struct{}{}
	}
	id := fmt.Sprintf("r%d", c.seq.Add(1))
	start := time.Now()
	err := c.call(ctx, id, method, params, out)
	attrs := []any{
		slog.String("method", method),
		slog.String("id", id),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()),
	}
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	c.log.Debug("herdr call", attrs...)
	return err
}

func (c *Client) call(ctx context.Context, id, method string, params, out any) error {
	line, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("herdr encode %s: %w", method, err)
	}
	ch := make(chan response, 1)

	c.pendingMu.Lock()
	select {
	case <-c.done:
		c.pendingMu.Unlock()
		return c.disconnectedErr()
	default:
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	c.writeMu.Lock()
	_, err = c.conn.Write(append(line, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("herdr write %s: %w", method, err)
	}

	select {
	case r := <-ch:
		if r.Error != nil {
			return &APIError{Code: r.Error.Code, Message: r.Error.Message}
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("herdr decode %s result: %w", method, err)
		}
		return nil
	case <-c.done:
		return c.disconnectedErr()
	case <-ctx.Done():
		return fmt.Errorf("herdr %s: %w", method, ctx.Err())
	}
}

// disconnectedErr wraps the read loop's exit cause; call only after done
// is closed so readErr is stable.
func (c *Client) disconnectedErr() error {
	<-c.done
	if c.readErr != nil {
		return fmt.Errorf("%w: %v", domain.ErrDisconnected, c.readErr)
	}
	return domain.ErrDisconnected
}

// Ping checks the server and warns when the protocol differs from the one
// this adapter was written for; it never fails on a mismatch.
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var res pongResult
	if err := c.Call(ctx, "ping", nil, &res); err != nil {
		return Pong{}, err
	}
	if res.Protocol != protocolVersion {
		c.log.Warn("herdr protocol mismatch",
			slog.Int("got", res.Protocol),
			slog.Int("want", protocolVersion),
			slog.String("version", res.Version))
	}
	return Pong{Version: res.Version, Protocol: res.Protocol}, nil
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20) // agent.read results can be large
	for sc.Scan() {
		var r response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.ID == "" {
			c.log.Debug("herdr ignoring line without id", slog.Int("bytes", len(sc.Bytes())))
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[r.ID]
		c.pendingMu.Unlock()
		if ok {
			ch <- r
		} else {
			c.log.Debug("herdr reply for unknown id", slog.String("id", r.ID))
		}
	}
	err := sc.Err()
	if err == nil {
		err = fmt.Errorf("connection closed")
	}
	c.readErr = err

	c.pendingMu.Lock()
	close(c.done)
	n := len(c.pending)
	c.pendingMu.Unlock()
	_ = c.Close()
	c.log.Warn("herdr connection lost", slog.String("err", err.Error()), slog.Int("pending", n))
}
