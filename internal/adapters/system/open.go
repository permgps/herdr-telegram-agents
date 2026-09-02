package system

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// openTimeout bounds the URL opener; the launchers return at once.
const openTimeout = 5 * time.Second

// OpenURL opens url with the desktop's default handler: `open` on macOS,
// `xdg-open` elsewhere on Unix, `rundll32 url.dll,FileProtocolHandler` on
// Windows. It is best effort for the setup popup, where text cannot be
// selected; the caller prints the link as well.
func OpenURL(ctx context.Context, url string) error {
	name, args, err := openCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// openCommand picks the launcher for goos. Only http(s) links are opened,
// so a stray value can never run as a local command.
func openCommand(goos, url string) (string, []string, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return "", nil, errors.New("only http(s) links can be opened")
	}
	switch goos {
	case "darwin":
		return "open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	default:
		return "xdg-open", []string{url}, nil
	}
}
