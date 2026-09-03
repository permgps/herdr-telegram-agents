package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// OptionsFileName is the operator-editable options file under the config
// dir, next to config.json.
const OptionsFileName = "options.json"

// optionsVersion is the on-disk format version.
const optionsVersion = 1

// optionsFile is the on-disk shape: every known key under "values" encoded
// by its kind (JSON bool for KindBool, JSON string otherwise). Unknown keys
// are kept as raw JSON so a newer build's options survive a save by an
// older one.
type optionsFile struct {
	Version int                        `json:"version"`
	Values  map[string]json.RawMessage `json:"values"`
}

// OptionsStore implements domain.OptionsStore over CONFIG_DIR/options.json.
type OptionsStore struct {
	path string
	log  *slog.Logger

	mu    sync.Mutex
	extra map[string]json.RawMessage
}

var _ domain.OptionsStore = (*OptionsStore)(nil)

// NewOptionsStore returns a store for options.json inside dir.
func NewOptionsStore(dir string, log *slog.Logger) *OptionsStore {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &OptionsStore{path: filepath.Join(dir, OptionsFileName), log: log, extra: map[string]json.RawMessage{}}
}

// Path returns the file the store reads and writes.
func (s *OptionsStore) Path() string { return s.path }

// Load reads the file. A missing file yields the defaults; a value of the
// wrong type is skipped with a warning; unknown keys are remembered for
// Save. A file that does not parse is an error so the caller can decide
// to run with defaults.
func (s *OptionsStore) Load(context.Context) (domain.Options, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.log.Debug("options file missing, using defaults", slog.String("path", s.path))
		return domain.DefaultOptions(), nil
	}
	if err != nil {
		return domain.DefaultOptions(), fmt.Errorf("read options: %w", err)
	}
	var f optionsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return domain.DefaultOptions(), fmt.Errorf("options %s: %w", s.path, err)
	}
	opts := domain.DefaultOptions()
	known, unknown := 0, 0
	s.extra = map[string]json.RawMessage{}
	for key, raw := range f.Values {
		spec, ok := domain.LookupOption(key)
		if !ok {
			s.extra[key] = raw
			unknown++
			continue
		}
		value, err := decodeOptionValue(spec.Kind, raw)
		if err != nil {
			s.log.Warn("option ignored", slog.String("key", key), slog.String("reason", err.Error()))
			continue
		}
		next, err := opts.With(key, value)
		if err != nil {
			s.log.Warn("option ignored", slog.String("key", key), slog.String("reason", err.Error()))
			continue
		}
		opts = next
		known++
	}
	s.log.Debug("options loaded",
		slog.String("path", s.path), slog.Int("version", f.Version), slog.Int("known", known), slog.Int("unknown", unknown))
	return opts, nil
}

// Save writes every known key plus the unknown ones seen by Load,
// atomically with mode 0600.
func (s *OptionsStore) Save(_ context.Context, o domain.Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := optionsFile{Version: optionsVersion, Values: make(map[string]json.RawMessage, len(domain.OptionSpecs)+len(s.extra))}
	for key, raw := range s.extra {
		f.Values[key] = raw
	}
	for _, spec := range domain.OptionSpecs {
		raw, err := encodeOptionValue(spec.Kind, o.String(spec.Key))
		if err != nil {
			return fmt.Errorf("encode option %q: %w", spec.Key, err)
		}
		f.Values[spec.Key] = raw
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode options: %w", err)
	}
	if err := writeAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save options: %w", err)
	}
	s.log.Debug("options saved", slog.String("path", s.path), slog.Int("bytes", len(data)), slog.Int("keys", len(f.Values)))
	return nil
}

// decodeOptionValue turns raw JSON into the string form the domain keeps.
// A bool accepts a JSON bool or the strings "true"/"false".
func decodeOptionValue(kind domain.OptionKind, raw json.RawMessage) (string, error) {
	switch kind {
	case domain.KindBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return strconv.FormatBool(b), nil
		}
		var str string
		if err := json.Unmarshal(raw, &str); err == nil && (str == "true" || str == "false") {
			return str, nil
		}
		return "", fmt.Errorf("want a bool, got %s", raw)
	default:
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return "", fmt.Errorf("want a string, got %s", raw)
		}
		return str, nil
	}
}

func encodeOptionValue(kind domain.OptionKind, value string) (json.RawMessage, error) {
	if kind == domain.KindBool {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	}
	return json.Marshal(value)
}
