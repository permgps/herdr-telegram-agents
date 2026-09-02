package herdr

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// cliTimeout bounds one herdr CLI invocation.
const cliTimeout = 10 * time.Second

// CLI runs one-shot herdr commands through the binary Herdr names in
// HERDR_BIN_PATH. The socket has no call for opening plugin panes, so this
// is the only way an action can show a pane.
type CLI struct {
	bin  string
	root string
	path string
	log  *slog.Logger
}

var _ domain.PaneOpener = (*CLI)(nil)

// NewCLI returns a runner for the herdr binary at bin. root is the plugin
// checkout (HERDR_PLUGIN_ROOT) and path the caller's PATH; both may be
// empty, in which case panes are opened with Herdr's own environment.
func NewCLI(bin, root, path string, log *slog.Logger) *CLI {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CLI{bin: bin, root: root, path: path, log: log}
}

// OpenPane runs `herdr plugin pane open --plugin <id> --entrypoint <pane>`.
// The manifest decides placement and size; stderr becomes the error text.
//
// Herdr 0.7.5 spawns pane commands like a terminal program: command[0] is
// looked up in PATH, not against the plugin directory the way actions and
// hooks are (verified 2026-09-02: "No viable candidates found in PATH").
// The pane inherits the PATH given here, so the plugin root goes first and
// the manifest's relative "bin/herdr-tg" resolves to <root>/bin/herdr-tg.
func (c *CLI) OpenPane(ctx context.Context, pluginID, entrypoint string) error {
	args := []string{"plugin", "pane", "open", "--plugin", pluginID, "--entrypoint", entrypoint}
	if c.root != "" {
		args = append(args, "--env", "PATH="+panePath(c.root, c.path))
	}
	return c.run(ctx, args)
}

// panePath prepends root to a PATH list.
func panePath(root, path string) string {
	if path == "" {
		return root
	}
	return root + string(os.PathListSeparator) + path
}

func (c *CLI) run(ctx context.Context, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	c.log.Debug("herdr cli", slog.String("bin", c.bin), slog.String("args", strings.Join(args, " ")),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()), slog.Any("err", err))
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		c.log.Warn("herdr cli failed", slog.String("args", strings.Join(args, " ")), slog.String("stderr", msg))
		if msg != "" {
			return fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
