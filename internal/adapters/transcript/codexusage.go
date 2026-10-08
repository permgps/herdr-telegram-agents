package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// codexUsageDays is how many days before today the usage scan looks
	// back; a Codex rate-limit snapshot older than its weekly window says
	// nothing anyway.
	codexUsageDays = 7
	// codexUsageFiles caps the rollouts opened per scan, newest first.
	codexUsageFiles = 8
	// codexUsageScan is the tail read per rollout; token_count records
	// follow every model response, so the newest one is near the end.
	codexUsageScan = 1 << 20
	// codexUsageDirEntries caps the entries read from one day directory.
	codexUsageDirEntries = 4096
	// codexUsageLimitID is the account-wide limit; other ids are scoped
	// to a model or feature and are not shown.
	codexUsageLimitID = "codex"
)

var (
	codexMarkTokenCount = []byte(`"token_count"`)
	codexMarkRateLimits = []byte(`"rate_limits":{`)
)

// codexUsageRecord is the part of a token_count event the usage scan reads.
type codexUsageRecord struct {
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			LimitID   string            `json:"limit_id"`
			Primary   *codexUsageWindow `json:"primary"`
			Secondary *codexUsageWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// codexUsageWindow is one Codex rate-limit window. Which slot (primary or
// secondary) holds the 5-hour or the weekly window depends on the plan,
// so the label comes from window_minutes.
type codexUsageWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// CodexUsage implements domain.UsageSource from Codex's own session files.
// Rate limits belong to the account, not to a pane, so unlike CodexReader
// it reads the newest rollout of any session: every token_count event
// carries the account's windows as of that response. Paths and file
// contents are never logged.
type CodexUsage struct {
	home func() (string, error)
	now  func() time.Time
	log  *slog.Logger
}

var _ domain.UsageSource = (*CodexUsage)(nil)

// NewCodexUsage reads the current user's ~/.codex/sessions.
func NewCodexUsage(log *slog.Logger) *CodexUsage {
	return newCodexUsage(os.UserHomeDir, time.Now, log)
}

func newCodexUsage(home func() (string, error), now func() time.Time, log *slog.Logger) *CodexUsage {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CodexUsage{home: home, now: now, log: log}
}

// codexUsageFile is a rollout candidate found in a day directory.
type codexUsageFile struct {
	rel  string
	info os.FileInfo
}

// Usage returns the windows of the newest token_count event with rate
// limits among the newest rollouts of the last week. No sessions directory
// or no such event is "no data", not an error.
func (c *CodexUsage) Usage(ctx context.Context) (domain.Usage, bool, error) {
	home, err := c.home()
	if err != nil {
		return domain.Usage{}, false, fmt.Errorf("codex usage: home directory: %w", err)
	}
	root, err := os.OpenRoot(filepath.Join(home, codexHomeDir, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		c.log.Debug("codex usage scan", slog.Int("files", 0), slog.Bool("hit", false), slog.String("reason", "no sessions directory"))
		return domain.Usage{}, false, nil
	}
	if err != nil {
		return domain.Usage{}, false, errors.New("codex usage: sessions directory could not be opened")
	}
	defer root.Close()

	files := c.candidates(root)
	opened := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return domain.Usage{}, false, err
		}
		opened++
		u, ok := c.scanFile(root, f)
		if ok {
			c.log.Debug("codex usage scan", slog.Int("files", opened), slog.Bool("hit", true), slog.Int("windows", len(u.Windows)))
			return u, true, nil
		}
	}
	c.log.Debug("codex usage scan", slog.Int("files", opened), slog.Bool("hit", false))
	return domain.Usage{}, false, nil
}

// candidates lists the regular rollout files of the scanned days, newest
// modification first, at most codexUsageFiles of them. Unreadable
// directories are skipped: a missing day is the normal case.
func (c *CodexUsage) candidates(root *os.Root) []codexUsageFile {
	var files []codexUsageFile
	for _, dir := range codexUsageDayDirs(c.now()) {
		d, err := codexOpenDir(root, dir)
		if err != nil {
			continue
		}
		entries, _ := d.ReadDir(codexUsageDirEntries)
		_ = d.Close()
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") || !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, codexUsageFile{rel: filepath.Join(dir, name), info: info})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].info.ModTime().After(files[j].info.ModTime()) })
	if len(files) > codexUsageFiles {
		files = files[:codexUsageFiles]
	}
	return files
}

// codexUsageDayDirs names the day directories from tomorrow back to
// codexUsageDays before today, in UTC and local time, newest first.
func codexUsageDayDirs(now time.Time) []string {
	seen := map[string]bool{}
	var dirs []string
	for days := -1; days <= codexUsageDays; days++ {
		for _, loc := range []*time.Location{time.Local, time.UTC} {
			d := now.In(loc).AddDate(0, 0, -days)
			dir := filepath.Join(d.Format("2006"), d.Format("01"), d.Format("02"))
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// scanFile walks one rollout back for the newest token_count event with
// account rate limits. A file that changed between listing and opening,
// or cannot be read, is skipped.
func (c *CodexUsage) scanFile(root *os.Root, cand codexUsageFile) (domain.Usage, bool) {
	f, info, err := openRegularSeen(root, cand.rel, cand.info)
	if err != nil {
		return domain.Usage{}, false
	}
	defer f.Close()
	var found domain.Usage
	_, err = walkBack(codexReadAt{f}, info.Size(), codexUsageScan, func(line []byte) error {
		u, ok := codexUsageFromLine(line)
		if !ok {
			return nil
		}
		found = u
		return errStop
	})
	if errors.Is(err, errStop) {
		return found, true
	}
	return domain.Usage{}, false
}

// codexUsageFromLine decodes one rollout line when it is a token_count
// event carrying the account-wide limits with at least one window. A
// half-written line fails to decode and is skipped.
func codexUsageFromLine(line []byte) (domain.Usage, bool) {
	if !bytes.Contains(line, codexMarkTokenCount) || !bytes.Contains(line, codexMarkRateLimits) {
		return domain.Usage{}, false
	}
	var rec codexUsageRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return domain.Usage{}, false
	}
	rl := rec.Payload.RateLimits
	if rec.Payload.Type != "token_count" || rl == nil {
		return domain.Usage{}, false
	}
	if rl.LimitID != "" && rl.LimitID != codexUsageLimitID {
		return domain.Usage{}, false
	}
	raw := make([]*codexUsageWindow, 0, 2)
	for _, w := range []*codexUsageWindow{rl.Primary, rl.Secondary} {
		if w != nil && w.WindowMinutes > 0 && w.ResetsAt > 0 {
			raw = append(raw, w)
		}
	}
	if len(raw) == 0 {
		return domain.Usage{}, false
	}
	// Shorter windows first, so a 5-hour window always leads.
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].WindowMinutes < raw[j].WindowMinutes })
	u := domain.Usage{Provider: domain.UsageCodex, ObservedAt: parseStamp(rec.Timestamp)}
	for _, w := range raw {
		u.Windows = append(u.Windows, domain.UsageWindow{
			Label:       domain.WindowLabel(w.WindowMinutes),
			UsedPercent: w.UsedPercent,
			ResetsAt:    codexUnix(w.ResetsAt),
		})
	}
	return u, true
}
