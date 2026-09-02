package cli

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// notifyRecorder captures the notifications a subcommand sends.
type notifyRecorder struct {
	mu     sync.Mutex
	bodies []string
	err    error
}

func (n *notifyRecorder) notify(_ context.Context, _ compose.PluginEnv, body string, _ *slog.Logger) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.bodies = append(n.bodies, body)
	return n.err
}

func (n *notifyRecorder) Bodies() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.bodies...)
}

// fakeSupervisor scripts the supervisor port for startup and action tests.
type fakeSupervisor struct {
	status   compose.DaemonStatus
	startPID int
	already  bool
	err      error
	calls    []string
}

func (f *fakeSupervisor) Status() compose.DaemonStatus {
	f.calls = append(f.calls, "status")
	return f.status
}

func (f *fakeSupervisor) Start(context.Context) (int, bool, error) {
	f.calls = append(f.calls, "start")
	return f.startPID, f.already, f.err
}

func (f *fakeSupervisor) Stop(context.Context) error {
	f.calls = append(f.calls, "stop")
	return f.err
}

func (f *fakeSupervisor) Restart(context.Context) (int, error) {
	f.calls = append(f.calls, "restart")
	return f.startPID, f.err
}

func (f *fakeSupervisor) Resync() error {
	f.calls = append(f.calls, "resync")
	return f.err
}

// testEnv points the wiring at temporary config and state directories and
// an unreachable socket, records notifications, and restores the real
// wiring afterwards.
func testEnv(t *testing.T) (compose.PluginEnv, *notifyRecorder) {
	t.Helper()
	env := compose.PluginEnv{
		PluginID:    "permgps.telegram-agents",
		ConfigDir:   t.TempDir(),
		StateDir:    t.TempDir(),
		SocketPath:  "/nonexistent/herdr.sock",
		BinPath:     "herdr",
		InsideHerdr: true,
	}
	rec := &notifyRecorder{}
	saved := wire
	t.Cleanup(func() { wire = saved })
	wire.env = func() (compose.PluginEnv, error) { return env, nil }
	wire.notify = rec.notify
	return env, rec
}

// saveConfig writes a valid config into the test environment.
func saveConfig(t *testing.T, env compose.PluginEnv) domain.Config {
	t.Helper()
	cfg := domain.Config{Version: domain.ConfigVersion, BotToken: "123:abc", BotUsername: "agents_bot",
		ChatID: -1001, ChatTitle: "Agents", OperatorIDs: []int64{7}}
	if err := compose.ConfigStore(env, nil).Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(args, "test", &out, &errb)
	return code, out.String(), errb.String()
}
