package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
)

const (
	// logsTailLines is how much history the logs pane shows first.
	logsTailLines = 100
	// logsPollInterval is how often the pane looks for new log lines.
	logsPollInterval = 500 * time.Millisecond
	// logFileName mirrors logging.LogFileName; the cli cannot import it.
	logFileName = "daemon.log"
)

// runLogsPane is the [[panes]] overlay: last lines of daemon.log rendered
// for humans, then follow until Ctrl-C, Ctrl-D, Esc or q.
func runLogsPane(rc *runContext, _ []string) int {
	rc, restore := rawPane(rc)
	defer restore()
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg logs-pane: %v\n", err)
		return exitError
	}
	path := filepath.Join(env.StateDir, logFileName)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchQuitKeys(rc.stdin, cancel)

	fmt.Fprintf(rc.stdout, "%s (Esc or q to close)\n", path)
	t := newTailer(path, logsPollInterval)
	if err := t.run(ctx, rc.stdout, logsTailLines); err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg logs-pane: %v\n", err)
		return exitError
	}
	rc.log.Debug("logs pane closed", slog.String("path", path))
	return exitOK
}

// watchQuitKeys cancels when stdin delivers Esc or q, or closes. A raw
// terminal (rawPane) delivers Ctrl-C and Ctrl-D as bytes, not as a signal
// and an end of input, so they count as well.
func watchQuitKeys(in io.Reader, cancel context.CancelFunc) {
	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		for _, b := range buf[:n] {
			if b == 0x1b || b == 'q' || b == 'Q' || b == 0x03 || b == 0x04 {
				cancel()
				return
			}
		}
		if err != nil {
			cancel()
			return
		}
	}
}

// tailer follows one file, surviving rotation: when the file shrinks or is
// replaced it reopens from the start of the new file.
type tailer struct {
	path     string
	interval time.Duration
	file     *os.File
	info     os.FileInfo
	offset   int64
	partial  string
}

func newTailer(path string, interval time.Duration) *tailer {
	return &tailer{path: path, interval: interval}
}

// run prints the last n lines, then follows until ctx ends. A missing
// file is not an error: the pane waits for the daemon to create it.
func (t *tailer) run(ctx context.Context, out io.Writer, n int) error {
	defer t.close()
	if err := t.open(); err != nil && !os.IsNotExist(err) {
		return err
	}
	if t.file != nil {
		lines, partial, end, err := lastLines(t.file, n)
		if err != nil {
			return err
		}
		for _, l := range lines {
			fmt.Fprintln(out, renderLogLine(l))
		}
		t.partial = partial
		t.offset = end
	} else {
		fmt.Fprintln(out, "(no log file yet)")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(t.interval):
			if err := t.poll(out); err != nil {
				return err
			}
		}
	}
}

func (t *tailer) open() error {
	// Shared open: on Windows a plain os.Open would keep the daemon from
	// renaming the file when it rotates the log.
	f, err := compose.OpenShared(t.path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	t.file, t.info, t.offset, t.partial = f, info, 0, ""
	return nil
}

func (t *tailer) close() {
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
}

// poll prints whatever was appended since the last call, reopening after
// truncation or rotation.
func (t *tailer) poll(out io.Writer) error {
	cur, err := os.Stat(t.path)
	switch {
	case os.IsNotExist(err):
		return nil // rotated away and not recreated yet
	case err != nil:
		return err
	}
	if t.file == nil || !os.SameFile(cur, t.info) || cur.Size() < t.offset {
		t.close()
		if err := t.open(); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
	}
	if cur.Size() == t.offset {
		return nil
	}
	if _, err := t.file.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(t.file)
	if err != nil {
		return err
	}
	t.offset += int64(len(data))
	text := t.partial + string(data)
	lines := strings.Split(text, "\n")
	t.partial = lines[len(lines)-1]
	for _, l := range lines[:len(lines)-1] {
		fmt.Fprintln(out, renderLogLine(l))
	}
	return nil
}

// renderLogLine turns one slog JSON line into `15:04:05 LEVEL msg k=v`.
// Lines that are not JSON objects are printed as they are. Either way
// control and bidi characters are escaped (escapeUnsafe): the log holds
// strangers' Telegram names, and decoding the JSON turns \u001b back into
// a real ESC.
func renderLogLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "{") {
		return escapeUnsafe(line)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return escapeUnsafe(line)
	}
	ts := ""
	if raw, ok := rec["time"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			ts = parsed.Local().Format("15:04:05")
		} else {
			ts = raw
		}
	}
	level, _ := rec["level"].(string)
	msg, _ := rec["msg"].(string)
	keys := make([]string, 0, len(rec))
	for k := range rec {
		switch k {
		case "time", "level", "msg":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	if ts != "" {
		b.WriteString(escapeUnsafe(ts))
		b.WriteByte(' ')
	}
	if level != "" {
		b.WriteString(escapeUnsafe(level))
		b.WriteByte(' ')
	}
	b.WriteString(escapeUnsafe(msg))
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(escapeUnsafe(k))
		b.WriteByte('=')
		b.WriteString(renderValue(rec[k]))
	}
	return b.String()
}

func renderValue(v any) string {
	switch x := v.(type) {
	case string:
		if strings.ContainsAny(x, " \t\"") || strings.ContainsFunc(x, unsafeRune) {
			return strconv.Quote(x)
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case nil:
		return "null"
	default:
		data, err := json.Marshal(x)
		if err != nil {
			return escapeUnsafe(fmt.Sprint(x))
		}
		return escapeUnsafe(string(data))
	}
}

// unsafeRune reports runes a terminal may act on instead of printing: C0,
// DEL and C1 controls (ESC starts sequences that retitle the window or
// clear the screen; C1 CSI does the same in one rune) and the bidi
// controls that reorder what follows.
func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == '\u061c', r == '\u200e', r == '\u200f',
		r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	}
	return false
}

// escapeUnsafe replaces every unsafeRune with a visible \xNN or \uNNNN.
func escapeUnsafe(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unsafeRune(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
