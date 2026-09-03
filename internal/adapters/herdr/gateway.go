package herdr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// eventBuffer bounds how many stream events may queue before the stream
// goroutine blocks; the application drains this channel continuously.
const eventBuffer = 64

// Gateway implements domain.HerdrGateway over short-lived request
// connections and one long-lived subscription stream to the Herdr socket.
//
// Every request dials its own connection because Herdr closes it after
// one reply. When the dial fails the request is retried once after a
// back-off pause before giving up with ErrDisconnected; nothing is retried
// once the request has been written. The stream reconnects on its own and
// reports every gap as a StreamReset event.
type Gateway struct {
	dial    dialFunc
	path    string
	log     *slog.Logger
	backoff Backoff

	stream *Stream
	events chan domain.Event

	runMu  sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	_ domain.HerdrGateway = (*Gateway)(nil)
	_ domain.HerdrProber  = (*Gateway)(nil)
)

// NewGateway prepares a gateway for the socket at path; call Start to
// connect. A zero Backoff falls back to DefaultBackoff.
func NewGateway(path string, log *slog.Logger, backoff Backoff) *Gateway {
	if backoff == (Backoff{}) {
		backoff = DefaultBackoff
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Gateway{
		dial:    dial,
		path:    path,
		log:     log,
		backoff: backoff,
		stream:  NewStream(dial, path, log, backoff),
		events:  make(chan domain.Event, eventBuffer),
	}
}

// Start pings the server and launches the stream goroutine. It fails when
// the socket cannot be reached so callers learn about a missing Herdr
// right away.
func (g *Gateway) Start(ctx context.Context) error {
	g.runMu.Lock()
	defer g.runMu.Unlock()
	if g.cancel != nil {
		return errors.New("herdr gateway already started")
	}
	pong, err := g.Ping(ctx)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		_ = g.stream.Run(runCtx, g.events)
	}()
	g.log.Info("herdr gateway started",
		slog.String("socket", g.path),
		slog.String("herdr_version", pong.Version),
		slog.Int("protocol", pong.Protocol))
	return nil
}

// Ping asks the server for its version and protocol on a fresh
// connection; it works before Start and without a stream, so the doctor
// can probe the socket with nothing else running.
func (g *Gateway) Ping(ctx context.Context) (domain.HerdrInfo, error) {
	pong, err := ping(ctx, g.dial, g.path, g.log)
	if err != nil {
		return domain.HerdrInfo{}, err
	}
	g.log.Debug("herdr ping", slog.String("socket", g.path), slog.String("herdr_version", pong.Version), slog.Int("protocol", pong.Protocol))
	return domain.HerdrInfo{Version: pong.Version, Protocol: pong.Protocol}, nil
}

// Close stops the stream, waits for it and closes the events channel. It
// is safe to call more than once.
func (g *Gateway) Close() error {
	g.runMu.Lock()
	defer g.runMu.Unlock()
	if g.cancel == nil {
		return nil
	}
	g.cancel()
	g.cancel = nil
	g.wg.Wait()
	close(g.events)
	return nil
}

// Events returns the stream channel; it is closed by Close.
func (g *Gateway) Events() <-chan domain.Event {
	return g.events
}

// WatchPanes replaces the set of panes whose status changes are streamed.
func (g *Gateway) WatchPanes(_ context.Context, paneIDs []string) error {
	g.stream.SetPanes(paneIDs)
	g.log.Debug("herdr watch panes", slog.Int("count", len(paneIDs)))
	return nil
}

// ListAgents returns every agent Herdr currently tracks, with the labels of
// its workspace and tab resolved from workspace.list and tab.list (AgentInfo
// carries only ids). A failed label lookup fails the whole listing so a
// pass never sees agents without labels and renames topics back and forth.
func (g *Gateway) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	var res agentListResult
	if err := g.call(ctx, "agent.list", "", nil, &res); err != nil {
		return nil, err
	}
	if len(res.Agents) == 0 {
		return []domain.Agent{}, nil
	}
	var wsRes workspaceListResult
	if err := g.call(ctx, "workspace.list", "", nil, &wsRes); err != nil {
		return nil, err
	}
	var tabRes tabListResult
	if err := g.call(ctx, "tab.list", "", nil, &tabRes); err != nil {
		return nil, err
	}
	wsLabel := make(map[string]string, len(wsRes.Workspaces))
	for _, w := range wsRes.Workspaces {
		wsLabel[w.WorkspaceID] = strings.TrimSpace(w.Label)
	}
	tabLabel := make(map[string]string, len(tabRes.Tabs))
	for _, t := range tabRes.Tabs {
		tabLabel[t.TabID] = strings.TrimSpace(t.Label)
	}
	agents := make([]domain.Agent, 0, len(res.Agents))
	for _, a := range res.Agents {
		d := toDomainAgent(a)
		d.WorkspaceLabel = wsLabel[d.WorkspaceID]
		d.TabLabel = tabLabel[d.TabID]
		agents = append(agents, d)
	}
	return agents, nil
}

// ReadScreen returns plain text from the agent's terminal.
func (g *Gateway) ReadScreen(ctx context.Context, target string, source domain.ScreenSource, lines int) (domain.Screen, error) {
	var res paneReadResult
	params := readParams{
		Target:    target,
		Source:    string(source),
		Lines:     lines,
		Format:    "text",
		StripANSI: true,
	}
	if err := g.call(ctx, "agent.read", target, params, &res); err != nil {
		return domain.Screen{}, err
	}
	return domain.Screen{
		Text:      res.Read.Text,
		Revision:  res.Read.Revision,
		Truncated: res.Read.Truncated,
	}, nil
}

// Prompt types text into the agent and submits it without waiting.
func (g *Gateway) Prompt(ctx context.Context, target, text string) error {
	return g.call(ctx, "agent.prompt", target, promptParams{Target: target, Text: text}, nil)
}

// SendKeys sends raw key names to the agent's terminal.
func (g *Gateway) SendKeys(ctx context.Context, target string, keys []string) error {
	if keys == nil {
		keys = []string{}
	}
	return g.call(ctx, "agent.send_keys", target, sendKeysParams{Target: target, Keys: keys}, nil)
}

// Focus brings the agent's pane to the front in Herdr.
func (g *Gateway) Focus(ctx context.Context, target string) error {
	return g.call(ctx, "agent.focus", target, focusParams{Target: target}, nil)
}

// Rename sets the agent's custom name; nil clears it.
func (g *Gateway) Rename(ctx context.Context, target string, name *string) error {
	return g.call(ctx, "agent.rename", target, renameParams{Target: target, Name: name}, nil)
}

// RenameTab sets a tab label through tab.rename.
func (g *Gateway) RenameTab(ctx context.Context, tabID, label string) error {
	return g.call(ctx, "tab.rename", tabID, tabRenameParams{TabID: tabID, Label: label}, nil)
}

// Notify shows a desktop notification through Herdr.
func (g *Gateway) Notify(ctx context.Context, title, body string, sound domain.NotifySound) error {
	return g.call(ctx, "notification.show", "", notifyParams{
		Title: title,
		Body:  body,
		Sound: wireSound(sound),
	}, nil)
}

// wireSound maps the port's sound choice to Herdr's vocabulary
// ("none" | "done" | "request").
func wireSound(s domain.NotifySound) string {
	switch s {
	case domain.NotifySoundNone:
		return "none"
	case domain.NotifySoundDefault:
		return "done"
	default:
		return ""
	}
}

// call runs one request, retrying a failed dial once, and translates
// server errors into domain terms.
func (g *Gateway) call(ctx context.Context, method, target string, params, out any) error {
	err := call(ctx, g.dial, g.path, g.log, method, params, out)
	if errors.Is(err, errDial) && ctx.Err() == nil {
		delay := g.backoff.Next(0)
		g.log.Warn("herdr unreachable, retrying once",
			slog.String("method", method),
			slog.String("err", err.Error()),
			slog.Int64("retry_ms", delay.Milliseconds()))
		if !sleep(ctx, delay) {
			return fmt.Errorf("herdr %s: %w", method, ctx.Err())
		}
		err = call(ctx, g.dial, g.path, g.log, method, params, out)
	}
	if errors.Is(err, errDial) {
		err = fmt.Errorf("%w: %v", domain.ErrDisconnected, err)
	}
	err = translateCallErr(method, err)
	attrs := []any{slog.String("method", method)}
	if target != "" {
		attrs = append(attrs, slog.String("target", target))
	}
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	g.log.Debug("herdr gateway call", attrs...)
	return err
}

// translateCallErr maps not_found to ErrAgentGone and tags other server
// errors with the method name.
func translateCallErr(method string, err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.Code == codeNotFound {
		return fmt.Errorf("herdr %s: %w", method, domain.ErrAgentGone)
	}
	return fmt.Errorf("herdr %s: %w", method, apiErr)
}
