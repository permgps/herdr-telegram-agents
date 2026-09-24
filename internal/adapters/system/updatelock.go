package system

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

var ErrUpdateLocked = errors.New("another update worker is active")

type updateOwner struct {
	PID       int       `json:"pid"`
	Nonce     string    `json:"nonce"`
	StartedAt time.Time `json:"started_at"`
}

// UpdateLock is an atomic directory lock independent of the checkout being
// replaced. Its owner record allows a later worker to recover a dead owner.
type UpdateLock struct {
	path  string
	alive func(int) bool
	log   *slog.Logger
	owner updateOwner
}

func NewUpdateLock(stateDir string, alive func(int) bool, log *slog.Logger) *UpdateLock {
	if alive == nil {
		alive = func(int) bool { return true }
	}
	return &UpdateLock{path: filepath.Join(stateDir, "update.lock"), alive: alive, log: log}
}

func (l *UpdateLock) Acquire() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := os.Mkdir(l.path, 0o700)
		if err == nil {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				_ = os.Remove(l.path)
				return err
			}
			owner := updateOwner{PID: os.Getpid(), Nonce: hex.EncodeToString(random[:]), StartedAt: time.Now().UTC()}
			data, _ := json.Marshal(owner)
			if err := os.WriteFile(filepath.Join(l.path, "owner.json"), data, 0o600); err != nil {
				_ = os.Remove(l.path)
				return err
			}
			l.owner = owner
			if l.log != nil {
				l.log.Info("update lock acquired", slog.Int("pid", owner.PID))
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create update lock: %w", err)
		}
		owner, readErr := l.readOwner()
		if readErr == nil && l.alive(owner.PID) {
			return ErrUpdateLocked
		}
		if readErr != nil {
			st, err := os.Stat(l.path)
			if err != nil {
				continue
			}
			if time.Since(st.ModTime()) < time.Minute {
				return ErrUpdateLocked
			}
		}
		// Rename first: only one contender can move this particular stale
		// directory. The winner removes it; everyone retries atomic mkdir.
		quarantine := fmt.Sprintf("%s.stale.%d.%d", l.path, os.Getpid(), time.Now().UnixNano())
		if err := os.Rename(l.path, quarantine); err == nil {
			_ = os.RemoveAll(quarantine)
			if l.log != nil {
				l.log.Warn("stale update lock removed", slog.Int("old_pid", owner.PID))
			}
		}
	}
	return ErrUpdateLocked
}

func (l *UpdateLock) Active() bool {
	owner, err := l.readOwner()
	return err == nil && l.alive(owner.PID)
}

func (l *UpdateLock) Release() error {
	if l.owner.Nonce == "" {
		return nil
	}
	owner, err := l.readOwner()
	if err != nil {
		return err
	}
	if owner.Nonce != l.owner.Nonce {
		return fmt.Errorf("update lock ownership changed")
	}
	if err := os.Remove(filepath.Join(l.path, "owner.json")); err != nil {
		return err
	}
	if err := os.Remove(l.path); err != nil {
		return err
	}
	l.owner = updateOwner{}
	if l.log != nil {
		l.log.Info("update lock released")
	}
	return nil
}

func (l *UpdateLock) readOwner() (updateOwner, error) {
	data, err := os.ReadFile(filepath.Join(l.path, "owner.json"))
	if err != nil {
		return updateOwner{}, err
	}
	var owner updateOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return owner, err
	}
	if owner.PID <= 0 || owner.Nonce == "" {
		return owner, fmt.Errorf("invalid update lock owner")
	}
	return owner, nil
}
