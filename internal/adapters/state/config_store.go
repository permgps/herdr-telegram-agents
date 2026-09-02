package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// ConfigFileName is the file the setup wizard writes under the config dir.
const ConfigFileName = "config.json"

// configFile is the on-disk shape of domain.Config. It is kept separate so
// the JSON tags never leak into the domain.
type configFile struct {
	Version      int       `json:"version"`
	BotToken     string    `json:"bot_token"`
	BotUsername  string    `json:"bot_username,omitempty"`
	ChatID       int64     `json:"chat_id"`
	ChatTitle    string    `json:"chat_title,omitempty"`
	OperatorIDs  []int64   `json:"operator_ids"`
	LogLevel     string    `json:"log_level,omitempty"`
	ConfiguredAt time.Time `json:"configured_at"`
}

// ConfigStore implements domain.ConfigStore over CONFIG_DIR/config.json.
type ConfigStore struct {
	path string
	log  *slog.Logger
}

var _ domain.ConfigStore = (*ConfigStore)(nil)

// NewConfigStore returns a store for config.json inside dir.
func NewConfigStore(dir string, log *slog.Logger) *ConfigStore {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ConfigStore{path: filepath.Join(dir, ConfigFileName), log: log}
}

// Path returns the file the store reads and writes.
func (s *ConfigStore) Path() string { return s.path }

// Load reads and validates the config. A missing file is ErrNotConfigured;
// a file that is readable by others still loads but is logged at warn.
func (s *ConfigStore) Load(context.Context) (domain.Config, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.log.Debug("config file missing", slog.String("path", s.path))
		return domain.Config{}, fmt.Errorf("config %s: %w", s.path, domain.ErrNotConfigured)
	}
	if err != nil {
		return domain.Config{}, fmt.Errorf("read config: %w", err)
	}
	s.warnPermissions()
	var f configFile
	if err := json.Unmarshal(data, &f); err != nil {
		return domain.Config{}, fmt.Errorf("config %s: %w: %v", s.path, domain.ErrNotConfigured, err)
	}
	cfg := domain.Config{
		Version:      f.Version,
		BotToken:     f.BotToken,
		BotUsername:  f.BotUsername,
		ChatID:       f.ChatID,
		ChatTitle:    f.ChatTitle,
		OperatorIDs:  f.OperatorIDs,
		LogLevel:     f.LogLevel,
		ConfiguredAt: f.ConfiguredAt,
	}
	if err := cfg.Validate(); err != nil {
		return domain.Config{}, fmt.Errorf("config %s: %w", s.path, err)
	}
	s.log.Debug("config loaded",
		slog.String("path", s.path), slog.Int64("chat_id", cfg.ChatID), slog.Int("operators", len(cfg.OperatorIDs)))
	return cfg, nil
}

// Save validates and writes the config atomically with mode 0600.
func (s *ConfigStore) Save(_ context.Context, cfg domain.Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	f := configFile{
		Version:      cfg.Version,
		BotToken:     cfg.BotToken,
		BotUsername:  cfg.BotUsername,
		ChatID:       cfg.ChatID,
		ChatTitle:    cfg.ChatTitle,
		OperatorIDs:  cfg.OperatorIDs,
		LogLevel:     cfg.LogLevel,
		ConfiguredAt: cfg.ConfiguredAt,
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := writeAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	s.log.Debug("config saved", slog.String("path", s.path), slog.Int64("chat_id", cfg.ChatID))
	return nil
}

// warnPermissions logs when the token file is readable by other users. The
// check is skipped on Windows, where Unix mode bits carry no meaning.
func (s *ConfigStore) warnPermissions() {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		s.log.Warn("config file is readable by others, expected 0600",
			slog.String("path", s.path), slog.String("mode", fmt.Sprintf("%04o", perm)))
	}
}
