package herdr

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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
	bin string
	log *slog.Logger
}

var _ domain.PaneOpener = (*CLI)(nil)

// NewCLI returns a runner for the herdr binary at bin.
func NewCLI(bin string, log *slog.Logger) *CLI {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CLI{bin: bin, log: log}
}

// OpenPane runs `herdr plugin pane open --plugin <id> --entrypoint <pane>`.
// The manifest decides placement and size; stderr becomes the error text.
func (c *CLI) OpenPane(ctx context.Context, pluginID, entrypoint string) error {
	args := []string{"plugin", "pane", "open", "--plugin", pluginID, "--entrypoint", entrypoint}
	return c.run(ctx, args)
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
