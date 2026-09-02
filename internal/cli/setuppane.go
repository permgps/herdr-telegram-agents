package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// setupRunner is the slice of compose.Setup the pane uses; an interface so
// tests can script the wizard outcome.
type setupRunner interface {
	Run(ctx context.Context) (domain.Config, bool, error)
}

// consoleUI implements domain.SetupUI over a line-oriented terminal. The
// token is typed visibly: the popup runs without raw-mode support and the
// pane closes as soon as setup ends.
type consoleUI struct {
	in   *bufio.Reader
	out  io.Writer
	open func(url string) error
}

var _ domain.SetupUI = (*consoleUI)(nil)

// newConsoleUI builds the UI; open launches a link in the browser and may
// be nil, in which case links are only printed.
func newConsoleUI(in io.Reader, out io.Writer, open func(url string) error) *consoleUI {
	if open == nil {
		open = func(string) error { return errors.New("no link opener") }
	}
	return &consoleUI{in: bufio.NewReader(in), out: out, open: open}
}

func (u *consoleUI) Print(text string) { fmt.Fprintln(u.out, text) }

func (u *consoleUI) OpenLink(url string) error { return u.open(url) }

func (u *consoleUI) Ask(prompt string) (string, error) {
	fmt.Fprint(u.out, prompt+" ")
	return u.readLine()
}

func (u *consoleUI) AskSecret(prompt string) (string, error) {
	fmt.Fprintln(u.out, "(the token is visible while you type; this pane closes when setup ends)")
	return u.Ask(prompt)
}

func (u *consoleUI) Confirm(prompt string) (bool, error) {
	line, err := u.Ask(prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func (u *consoleUI) Choose(prompt string, options []string) (int, error) {
	for i, o := range options {
		fmt.Fprintf(u.out, "  %d) %s\n", i+1, o)
	}
	for {
		line, err := u.Ask(fmt.Sprintf("%s [1-%d]", prompt, len(options)))
		if err != nil {
			return 0, err
		}
		n, convErr := strconv.Atoi(line)
		if convErr == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		fmt.Fprintf(u.out, "please enter a number between 1 and %d\n", len(options))
	}
}

// readLine returns one trimmed line; EOF without text is an error so the
// wizard stops when the pane's stdin closes.
func (u *consoleUI) readLine() (string, error) {
	line, err := u.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// waitEnter blocks until Enter or EOF so the popup does not vanish before
// the user reads the last line.
func (u *consoleUI) waitEnter() {
	fmt.Fprint(u.out, "press Enter to close ")
	_, _ = u.readLine()
}

// runSetupPane is the [[panes]] popup: it runs the wizard on stdin/stdout
// and then starts or restarts the daemon with the new configuration.
func runSetupPane(rc *runContext, _ []string) int {
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg setup-pane: %v\n", err)
		return exitError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui := newConsoleUI(rc.stdin, rc.stdout, func(url string) error { return wire.openURL(ctx, url) })

	// The popup is gone once the wizard ends, so its log also goes to
	// daemon.log where a failure can be read afterwards.
	log := rc.log
	if flog, closer, err := wire.fileLogger(env, ""); err != nil {
		rc.log.Warn("setup pane: file log unavailable", slog.String("err", err.Error()))
	} else {
		defer closer.Close()
		log = wire.teeLogger(rc.log, flog).With(slog.String("component", "setup"))
	}

	cfg, saved, err := wire.buildSetup(env, ui, log).Run(ctx)
	switch {
	case errors.Is(err, ErrSetupCancelled):
		ui.Print("Setup cancelled; nothing was changed.")
		ui.waitEnter()
		return exitOK
	case err != nil:
		rc.log.Warn("setup failed", slog.String("err", err.Error()))
		ui.Print("Setup failed: " + err.Error())
		ui.waitEnter()
		return exitError
	}
	rc.log.Debug("setup pane finished wizard", slog.Bool("saved", saved), slog.Int64("chat_id", cfg.ChatID))

	sup := wire.buildSupervisor(env, rc.log)
	actx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	switch {
	case sup.Status().Running:
		pid, err := sup.Restart(actx)
		if err != nil {
			ui.Print("Configuration saved, but the daemon could not be restarted: " + err.Error())
			ui.waitEnter()
			return exitError
		}
		ui.Print(fmt.Sprintf("Daemon restarted (pid %d) for group %q.", pid, cfg.ChatTitle))
	default:
		pid, _, err := sup.Start(actx)
		if err != nil {
			ui.Print("Configuration saved, but the daemon could not be started: " + err.Error())
			ui.waitEnter()
			return exitError
		}
		ui.Print(fmt.Sprintf("Daemon started (pid %d) for group %q.", pid, cfg.ChatTitle))
	}
	ui.waitEnter()
	return exitOK
}
