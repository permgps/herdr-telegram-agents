package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// errDial marks failures that happened before the request was written, so
// callers know a retry cannot duplicate a side effect.
var errDial = errors.New("herdr dial")

// callSeq numbers requests across connections for log correlation.
var callSeq atomic.Int64

// Pong is the result of a ping request.
type Pong struct {
	Version  string
	Protocol int
}

// call performs one request on a fresh connection and closes it.
//
// Herdr 0.7.5 answers exactly one request per connection and hangs up
// after the reply (verified against the live socket), so a dedicated
// short-lived connection per call is the only model that works. It also
// keeps working should a later version keep connections open. A nil
// params serialises as {} because Herdr requires the field. Server-side
// failures come back as *APIError.
func call(ctx context.Context, dial dialFunc, path string, log *slog.Logger, method string, params, out any) error {
	if params == nil {
		params = struct{}{}
	}
	id := fmt.Sprintf("r%d", callSeq.Add(1))
	start := time.Now()
	err := callOnce(ctx, dial, path, id, method, params, out)
	attrs := []any{
		slog.String("method", method),
		slog.String("id", id),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()),
	}
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	log.Debug("herdr call", attrs...)
	return err
}

func callOnce(ctx context.Context, dial dialFunc, path, id, method string, params, out any) error {
	line, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("herdr encode %s: %w", method, err)
	}
	conn, err := dial(ctx, path)
	if err != nil {
		return fmt.Errorf("%w %s: %w", errDial, path, err)
	}
	defer func() { _ = conn.Close() }()

	// Cancelling ctx closes the connection, which unblocks Write/Read.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	if _, err := conn.Write(append(line, '\n')); err != nil {
		return ctxErr(ctx, method, fmt.Errorf("herdr write %s: %w", method, err))
	}
	rd := bufio.NewReaderSize(conn, 64<<10)
	for {
		raw, err := rd.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				err = fmt.Errorf("%w: server closed before replying to %s", domain.ErrDisconnected, method)
			} else {
				err = fmt.Errorf("herdr read %s: %w", method, err)
			}
			return ctxErr(ctx, method, err)
		}
		var r response
		if json.Unmarshal(raw, &r) != nil || r.ID != id {
			continue // noise or a stray line for someone else
		}
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
	}
}

// ctxErr prefers the context's own error once it has been cancelled, since
// the transport error is then just a symptom of closing the connection.
func ctxErr(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("herdr %s: %w", method, ctx.Err())
	}
	return err
}

// ping checks the server and warns when the protocol differs from the one
// this adapter was written for; it never fails on a mismatch.
func ping(ctx context.Context, dial dialFunc, path string, log *slog.Logger) (Pong, error) {
	var res pongResult
	if err := call(ctx, dial, path, log, "ping", nil, &res); err != nil {
		return Pong{}, err
	}
	if res.Protocol != protocolVersion {
		log.Warn("herdr protocol mismatch",
			slog.Int("got", res.Protocol),
			slog.Int("want", protocolVersion),
			slog.String("version", res.Version))
	}
	return Pong{Version: res.Version, Protocol: res.Protocol}, nil
}
