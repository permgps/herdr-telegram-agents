package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// ControlFileName is the daemon's control socket inside the state
	// directory on Unix. Windows uses a named pipe instead; see
	// ControlPath.
	ControlFileName = "control.sock"
	// controlTimeout bounds one control exchange on both sides.
	controlTimeout = 2 * time.Second
	// controlMaxLine caps a command line so a stray writer cannot make the
	// daemon buffer without end.
	controlMaxLine = 256
)

// Control commands understood by the daemon.
const (
	ControlStop   = "stop"
	ControlResync = "resync"
	ControlStatus = "status"
)

// ControlHandlers are the daemon actions the channel exposes. Stop and
// Resync must not block; Status returns one line.
type ControlHandlers struct {
	Stop   func()
	Resync func()
	Status func() string
}

// ServeControl answers one command per connection until ctx is done, then
// closes the listener. It never returns an error: a broken connection is
// logged and the loop continues, because the daemon must survive whatever
// a client does.
func ServeControl(ctx context.Context, ln net.Listener, h ControlHandlers, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log.Info("control listening", slog.String("addr", ln.Addr().String()))
	defer log.Debug("control listener stopped")

	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = ln.Close()
	}()
	defer func() {
		close(done)
		wg.Wait()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn("control accept failed", slog.String("err", err.Error()))
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveControlConn(conn, h, log)
		}()
	}
}

// serveControlConn reads one command, answers it and closes the connection.
func serveControlConn(conn net.Conn, h ControlHandlers, log *slog.Logger) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))

	line, err := bufio.NewReader(io.LimitReader(conn, controlMaxLine)).ReadString('\n')
	cmd := strings.ToLower(strings.TrimSpace(line))
	if err != nil && cmd == "" {
		log.Warn("control read failed", slog.String("err", err.Error()))
		return
	}
	log.Debug("control command", slog.String("cmd", cmd))

	reply := "error: unknown command " + cmd
	switch cmd {
	case ControlStop:
		if h.Stop != nil {
			h.Stop()
		}
		reply = "ok"
	case ControlResync:
		if h.Resync != nil {
			h.Resync()
		}
		reply = "ok"
	case ControlStatus:
		reply = "ok"
		if h.Status != nil {
			reply = "ok " + strings.ReplaceAll(h.Status(), "\n", " ")
		}
	}
	if _, err := io.WriteString(conn, reply+"\n"); err != nil {
		log.Warn("control reply failed", slog.String("cmd", cmd), slog.String("err", err.Error()))
		return
	}
	log.Debug("control answered", slog.String("cmd", cmd), slog.String("reply", reply))
}

// SendControl sends one command to the daemon listening for stateDir and
// returns its reply without the "ok " prefix. A daemon that is not
// listening yields ErrControlUnavailable so callers can fall back.
func SendControl(ctx context.Context, stateDir, cmd string) (string, error) {
	conn, err := dialControl(ctx, ControlPath(stateDir))
	if err != nil {
		return "", fmt.Errorf("control %s: %w", cmd, domain.ErrControlUnavailable)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(controlTimeout))

	if _, err := io.WriteString(conn, cmd+"\n"); err != nil {
		return "", fmt.Errorf("control %s: write: %w", cmd, err)
	}
	line, err := bufio.NewReader(io.LimitReader(conn, controlMaxLine)).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", fmt.Errorf("control %s: read: %w", cmd, err)
	}
	if rest, ok := strings.CutPrefix(line, "ok"); ok {
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("control %s: %s", cmd, line)
}
