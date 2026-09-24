package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// MappingFileName is the file under the state dir holding the topic map.
const MappingFileName = "mapping.json"

type mappingFile struct {
	Version          int                         `json:"version"`
	ChatID           int64                       `json:"chat_id"`
	Topics           map[string]mappingFileEntry `json:"topics"`
	PendingCreates   map[string]bool             `json:"pending_creates,omitempty"`
	PendingDashboard bool                        `json:"pending_dashboard,omitempty"`
	// Dashboard is the pinned status message in General; omitted when
	// none exists so files written before the dashboard stay byte-equal.
	Dashboard int `json:"dashboard_message_id,omitempty"`
}

type mappingFileEntry struct {
	ThreadID    int       `json:"thread_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Closed      bool      `json:"closed"`
	Muted       bool      `json:"muted,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	AgentKind   string    `json:"agent_kind,omitempty"`
	LegacyNoCwd bool      `json:"legacy_no_cwd,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MappingStore implements domain.MappingStore over STATE_DIR/mapping.json.
type MappingStore struct {
	path string
	log  *slog.Logger
	now  func() time.Time
}

var _ domain.MappingStore = (*MappingStore)(nil)

// NewMappingStore returns a store for mapping.json inside dir.
func NewMappingStore(dir string, log *slog.Logger) *MappingStore {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &MappingStore{path: filepath.Join(dir, MappingFileName), log: log, now: time.Now}
}

// Path returns the file the store reads and writes.
func (s *MappingStore) Path() string { return s.path }

// BrokenFiles lists the mapping.json.broken-* backups Load left behind
// (base names only), so the doctor can point at them.
func (s *MappingStore) BrokenFiles() ([]string, error) {
	matches, err := filepath.Glob(s.path + ".broken-*")
	if err != nil {
		return nil, fmt.Errorf("list broken mapping files: %w", err)
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, filepath.Base(m))
	}
	s.log.Debug("broken mapping files", slog.Int("count", len(names)))
	return names, nil
}

// Load reads the mapping. A missing file yields an empty mapping. A file
// that cannot be decoded is moved aside as mapping.json.broken-<timestamp>
// and an empty mapping is returned, so one corrupt write never blocks the
// daemon; the reconciler recreates topics as needed.
func (s *MappingStore) Load(context.Context) (*domain.Mapping, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.log.Debug("mapping file missing, starting empty", slog.String("path", s.path))
		return domain.NewMapping(0), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}
	var f mappingFile
	if err := json.Unmarshal(data, &f); err != nil {
		backup := fmt.Sprintf("%s.broken-%s", s.path, s.now().UTC().Format("20060102T150405"))
		if renameErr := os.Rename(s.path, backup); renameErr != nil {
			return nil, fmt.Errorf("mapping %s is malformed (%v) and could not be moved aside: %w", s.path, err, renameErr)
		}
		s.log.Warn("mapping file malformed, moved aside and starting empty",
			slog.String("path", s.path), slog.String("backup", backup), slog.String("err", err.Error()))
		return domain.NewMapping(0), nil
	}
	m := domain.NewMapping(f.ChatID)
	if f.Version != 0 {
		m.Version = f.Version
	}
	m.Dashboard = f.Dashboard
	m.PendingDashboard = f.PendingDashboard
	for key, pending := range f.PendingCreates {
		if pending {
			m.PendingCreates[key] = true
		}
	}
	for key, e := range f.Topics {
		m.Topics[key] = &domain.TopicEntry{
			ThreadID:    e.ThreadID,
			Name:        e.Name,
			Status:      domain.Status(e.Status),
			Closed:      e.Closed,
			Muted:       e.Muted,
			Cwd:         e.Cwd,
			AgentKind:   e.AgentKind,
			LegacyNoCwd: e.LegacyNoCwd || (f.Version < domain.MappingVersion && e.Cwd == ""),
			UpdatedAt:   e.UpdatedAt,
		}
	}
	s.log.Debug("mapping loaded", slog.String("path", s.path), slog.Int64("chat_id", m.ChatID),
		slog.Int("entries", len(m.Topics)), slog.Int("dashboard_message_id", m.Dashboard))
	return m, nil
}

// Save writes the mapping atomically with mode 0644.
func (s *MappingStore) Save(ctx context.Context, m *domain.Mapping) error {
	version := m.Version
	if version < domain.MappingVersion {
		version = domain.MappingVersion
	}
	f := mappingFile{Version: version, ChatID: m.ChatID, Topics: make(map[string]mappingFileEntry, len(m.Topics)), Dashboard: m.Dashboard,
		PendingDashboard: m.PendingDashboard}
	if len(m.PendingCreates) > 0 {
		f.PendingCreates = make(map[string]bool, len(m.PendingCreates))
		for key, pending := range m.PendingCreates {
			if pending {
				f.PendingCreates[key] = true
			}
		}
	}
	for key, e := range m.Topics {
		f.Topics[key] = mappingFileEntry{
			ThreadID:    e.ThreadID,
			Name:        e.Name,
			Status:      string(e.Status),
			Closed:      e.Closed,
			Muted:       e.Muted,
			Cwd:         e.Cwd,
			AgentKind:   e.AgentKind,
			LegacyNoCwd: e.LegacyNoCwd,
			UpdatedAt:   e.UpdatedAt,
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mapping: %w", err)
	}
	if err := writeAtomic(s.path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("save mapping: %w", err)
	}
	if m.Version > 0 && m.Version < version {
		for key, e := range m.Topics {
			attrs := []slog.Attr{slog.Int("thread_id", e.ThreadID)}
			if k, ok := domain.ParseKey(key); ok {
				attrs = append(attrs, slog.String("pane", k.PaneID))
			}
			s.log.LogAttrs(ctx, slog.LevelInfo, "mapping entry migrated", attrs...)
		}
	}
	if m.Version < version {
		m.Version = version
	}
	s.log.Debug("mapping saved", slog.String("path", s.path), slog.Int("entries", len(m.Topics)), slog.Int("dashboard_message_id", m.Dashboard))
	return nil
}
