package testkit

import (
	"context"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// MemConfigStore keeps a config in memory. Load fails with ErrNotConfigured
// until something was saved (or seeded through Set).
type MemConfigStore struct {
	mu    sync.Mutex
	cfg   domain.Config
	has   bool
	saves int
	err   error
}

var _ domain.ConfigStore = (*MemConfigStore)(nil)

// NewMemConfigStore returns an empty store.
func NewMemConfigStore() *MemConfigStore { return &MemConfigStore{} }

// Set seeds the stored config without counting as a save.
func (s *MemConfigStore) Set(cfg domain.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.has = cfg, true
}

// Fail makes Load and Save return err until called with nil.
func (s *MemConfigStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// SaveCount returns how many times Save succeeded.
func (s *MemConfigStore) SaveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *MemConfigStore) Load(context.Context) (domain.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return domain.Config{}, s.err
	}
	if !s.has {
		return domain.Config{}, domain.ErrNotConfigured
	}
	return s.cfg, nil
}

func (s *MemConfigStore) Save(_ context.Context, cfg domain.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.cfg, s.has = cfg, true
	s.saves++
	return nil
}

// MemMappingStore keeps a deep copy of the last saved mapping so tests can
// tell what would survive a restart.
type MemMappingStore struct {
	mu    sync.Mutex
	saved *domain.Mapping
	saves int
	err   error
}

var _ domain.MappingStore = (*MemMappingStore)(nil)

// NewMemMappingStore returns an empty store.
func NewMemMappingStore() *MemMappingStore { return &MemMappingStore{} }

// Fail makes Save return err until called with nil.
func (s *MemMappingStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// SaveCount returns how many times Save succeeded.
func (s *MemMappingStore) SaveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

// Saved returns a copy of the last saved mapping, or nil.
func (s *MemMappingStore) Saved() *domain.Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyMapping(s.saved)
}

func (s *MemMappingStore) Load(context.Context) (*domain.Mapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saved == nil {
		return domain.NewMapping(0), nil
	}
	return copyMapping(s.saved), nil
}

func (s *MemMappingStore) Save(_ context.Context, m *domain.Mapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.saved = copyMapping(m)
	s.saves++
	return nil
}

func copyMapping(m *domain.Mapping) *domain.Mapping {
	if m == nil {
		return nil
	}
	out := domain.NewMapping(m.ChatID)
	out.Version = m.Version
	for k, e := range m.Topics {
		c := *e
		out.Topics[k] = &c
	}
	return out
}
