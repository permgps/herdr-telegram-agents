package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// defaultInboxMaxTotal is the total size the inbox keeps when MaxTotal is
// not set; it matches the default of domain.OptionInboxMaxTotalMB.
const defaultInboxMaxTotal = 500 << 20

// inboxSharedPrefix marks the files private recipients send. SaveShared
// adds it itself; owner names never start with it (domain.InboxFileName
// starts with a date).
const inboxSharedPrefix = "shared-"

// inboxSharedShare is the part of the total quota the shared files hold
// together: at most MaxTotal()/inboxSharedShare.
const inboxSharedShare = 4

// Inbox implements domain.InboxStore over STATE_DIR/inbox.
type Inbox struct {
	dir string
	log *slog.Logger
	now func() time.Time
	// MaxTotal is the most the inbox holds in bytes, read on every Save;
	// nil means defaultInboxMaxTotal.
	MaxTotal func() int64
	mu       sync.Mutex
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
// ".." component is refused. Room is made from the oldest files, shared
// ones included.
func (i *Inbox) Save(_ context.Context, name string, data []byte) (string, error) {
	return i.save(name, data, i.makeRoom)
}

// SaveShared is Save for a file a private recipient sent: it is stored as
// shared-<name>, and room is made only from the oldest shared files, which
// together stay within a quarter of the total quota. The owner's files are
// never deleted for it.
func (i *Inbox) SaveShared(_ context.Context, name string, data []byte) (string, error) {
	if unsafeInboxName(name) {
		return "", fmt.Errorf("inbox: unsafe name %q", name)
	}
	return i.save(inboxSharedPrefix+name, data, i.makeSharedRoom)
}

// unsafeInboxName reports a name with a path separator or a ".." component.
func unsafeInboxName(name string) bool {
	return name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".."
}

func (i *Inbox) save(name string, data []byte, makeRoom func(need int64) error) (string, error) {
	if unsafeInboxName(name) {
		return "", fmt.Errorf("inbox: unsafe name %q", name)
	}
	if err := os.MkdirAll(i.dir, inboxDirMode); err != nil {
		return "", fmt.Errorf("inbox: mkdir %s: %w", i.dir, err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := makeRoom(int64(len(data))); err != nil {
		return "", err
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

// inboxFile is one regular file of the inbox.
type inboxFile struct {
	path   string
	size   int64
	mod    time.Time
	shared bool
}

// limit is the total quota in bytes.
func (i *Inbox) limit() int64 {
	if i.MaxTotal != nil {
		return i.MaxTotal()
	}
	return defaultInboxMaxTotal
}

// list returns the inbox's regular files, oldest first, and their total
// size. Temp files of a Save in flight (dot names) are left out.
func (i *Inbox) list() ([]inboxFile, int64, error) {
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		return nil, 0, fmt.Errorf("inbox: list %s: %w", i.dir, err)
	}
	var files []inboxFile
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, inboxFile{filepath.Join(i.dir, e.Name()), info.Size(), info.ModTime(), strings.HasPrefix(e.Name(), inboxSharedPrefix)})
		total += info.Size()
	}
	sort.Slice(files, func(a, b int) bool { return files[a].mod.Before(files[b].mod) })
	return files, total, nil
}

// makeRoom deletes the oldest files until need more bytes fit under the
// total quota. A file larger than the whole quota is refused.
func (i *Inbox) makeRoom(need int64) error {
	limit := i.limit()
	if need > limit {
		i.log.Warn("[FIX] inbox file exceeds the total quota", slog.Int64("bytes", need), slog.Int64("quota", limit))
		return fmt.Errorf("inbox: %d bytes exceed the %d byte quota: %w", need, limit, domain.ErrFileTooBig)
	}
	files, total, err := i.list()
	if err != nil {
		return err
	}
	if total+need <= limit {
		return nil
	}
	for _, f := range files {
		if total+need <= limit {
			break
		}
		if err := os.Remove(f.path); err != nil {
			i.log.Warn("inbox delete failed", slog.String("path", f.path), slog.String("err", err.Error()))
			continue
		}
		total -= f.size
		i.log.Info("[FIX] inbox quota: oldest file deleted", slog.String("path", f.path), slog.Int64("bytes", f.size), slog.Int64("quota", limit))
	}
	if total+need > limit {
		return fmt.Errorf("inbox: no room for %d bytes under the %d byte quota: %w", need, limit, domain.ErrFileTooBig)
	}
	return nil
}

// makeSharedRoom deletes the oldest shared files until need more bytes fit
// both under the shared quota (a quarter of the total) and under the total
// quota. A file larger than the shared quota, or one that would need the
// owner's files deleted, is refused.
func (i *Inbox) makeSharedRoom(need int64) error {
	limit := i.limit()
	quota := limit / inboxSharedShare
	if need > quota {
		i.log.Warn("[FIX] inbox shared file exceeds the shared quota", slog.Int64("bytes", need), slog.Int64("quota", quota))
		return fmt.Errorf("inbox: %d bytes exceed the %d byte shared quota: %w", need, quota, domain.ErrFileTooBig)
	}
	files, total, err := i.list()
	if err != nil {
		return err
	}
	var shared int64
	for _, f := range files {
		if f.shared {
			shared += f.size
		}
	}
	// Even with every shared file gone the owner's files leave no room:
	// refuse without deleting anything.
	if total-shared+need > limit {
		i.log.Warn("[FIX] inbox shared save refused", slog.Int64("bytes", need), slog.Int64("shared_total", shared), slog.Int64("quota", quota),
			slog.String("reason", "owner_files"))
		return fmt.Errorf("inbox: no room for %d shared bytes under the %d byte quota: %w", need, limit, domain.ErrFileTooBig)
	}
	for _, f := range files {
		if shared+need <= quota && total+need <= limit {
			break
		}
		if !f.shared {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			i.log.Warn("inbox delete failed", slog.String("file", filepath.Base(f.path)), slog.String("err", err.Error()))
			continue
		}
		shared -= f.size
		total -= f.size
		i.log.Info("[FIX] inbox shared quota: oldest shared file deleted", slog.String("file", filepath.Base(f.path)), slog.Int64("bytes", f.size), slog.Int64("quota", quota))
	}
	if shared+need > quota || total+need > limit {
		i.log.Warn("[FIX] inbox shared save refused", slog.Int64("bytes", need), slog.Int64("shared_total", shared), slog.Int64("quota", quota),
			slog.String("reason", "no_room"))
		return fmt.Errorf("inbox: no room for %d shared bytes under the %d byte shared quota: %w", need, quota, domain.ErrFileTooBig)
	}
	return nil
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
