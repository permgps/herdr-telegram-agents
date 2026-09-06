package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// InboxDirName is the directory under the state dir holding the files
// operators send to topics.
const InboxDirName = "inbox"

// inboxDirMode and inboxFileMode keep attachments private to the user.
const (
	inboxDirMode  = 0o700
	inboxFileMode = 0o600
)

// Inbox implements domain.InboxStore over STATE_DIR/inbox.
type Inbox struct {
	dir string
	log *slog.Logger
	now func() time.Time
}

var _ domain.InboxStore = (*Inbox)(nil)

// NewInbox returns the inbox rooted at stateDir/inbox; the directory is
// created on the first Save.
func NewInbox(stateDir string, log *slog.Logger) *Inbox {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Inbox{dir: filepath.Join(stateDir, InboxDirName), log: log, now: time.Now}
}

// Dir returns the inbox directory.
func (i *Inbox) Dir() string { return i.dir }

// Save writes data as name inside the inbox (mode 0600, through a temp
// file and rename) and returns the absolute path. A name that is taken
// gets -2, -3 … before its extension. A name with a path separator or a
// ".." component is refused.
func (i *Inbox) Save(_ context.Context, name string, data []byte) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("inbox: unsafe name %q", name)
	}
	if err := os.MkdirAll(i.dir, inboxDirMode); err != nil {
		return "", fmt.Errorf("inbox: mkdir %s: %w", i.dir, err)
	}
	path, err := i.freePath(name)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, data, inboxFileMode); err != nil {
		return "", fmt.Errorf("inbox: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	i.log.Debug("inbox saved", slog.String("path", abs), slog.Int("bytes", len(data)))
	return abs, nil
}

// freePath returns dir/name, or dir/name-N.ext for the first N from 2 that
// does not exist yet.
func (i *Inbox) freePath(name string) (string, error) {
	path := filepath.Join(i.dir, name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; ; n++ {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", fmt.Errorf("inbox: stat %s: %w", path, err)
		}
		if n > 1000 {
			return "", fmt.Errorf("inbox: no free name for %q", name)
		}
		path = filepath.Join(i.dir, stem+"-"+strconv.Itoa(n)+ext)
	}
}

// Sweep deletes regular files in the inbox whose modification time is
// older than olderThan and returns how many went. Subdirectories and
// temp files of a Save in flight are left alone; a missing inbox counts
// as empty.
func (i *Inbox) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(i.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("inbox: list %s: %w", i.dir, err)
	}
	cutoff := i.now().Add(-olderThan)
	deleted := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(i.dir, e.Name())
		if err := os.Remove(path); err != nil {
			i.log.Warn("inbox delete failed", slog.String("path", path), slog.String("err", err.Error()))
			continue
		}
		deleted++
		i.log.Debug("inbox deleted", slog.String("path", path), slog.Int64("age_h", int64(i.now().Sub(info.ModTime()).Hours())))
	}
	i.log.Info("inbox sweep", slog.Int("deleted", deleted), slog.Int("kept", len(entries)-deleted), slog.Int64("older_than_h", int64(olderThan.Hours())))
	return deleted, nil
}
