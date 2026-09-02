package logging

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

const (
	// LogFileName is the daemon log under the state directory.
	LogFileName = "daemon.log"
	// RotateSize is the size at which daemon.log is rotated.
	RotateSize int64 = 5 << 20
	// RotateKeep is how many rotated files are kept (.1 and .2).
	RotateKeep = 2
)

// NewFileLogger returns a JSON logger writing to STATE_DIR/daemon.log with
// rotation, plus the closer that releases the file.
func NewFileLogger(stateDir string, level slog.Level) (*slog.Logger, io.Closer, error) {
	path := filepath.Join(stateDir, LogFileName)
	w, err := NewRotatingWriter(path, RotateSize, RotateKeep)
	if err != nil {
		return nil, nil, err
	}
	log := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
	log.Info("log opened", slog.String("path", path), slog.String("level", level.String()))
	return log, w, nil
}

// ParseLevel maps a LOG_LEVEL value to a slog.Level. Unknown or empty values
// fall back to info so a typo never silences or floods the log.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ResolveLevel picks the effective level: the environment value wins when
// set, then the config value, then info.
func ResolveLevel(envValue, configValue string) slog.Level {
	if strings.TrimSpace(envValue) != "" {
		return ParseLevel(envValue)
	}
	return ParseLevel(configValue)
}
