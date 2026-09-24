package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type UpdateStore struct {
	path string
	log  *slog.Logger
	Now  func() time.Time
}

func NewUpdateStore(dir string, log *slog.Logger) *UpdateStore {
	return &UpdateStore{path: filepath.Join(dir, "update.json"), log: log}
}

func (s *UpdateStore) Load(ctx context.Context) (domain.UpdateJob, error) {
	if err := ctx.Err(); err != nil {
		return domain.UpdateJob{}, err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return domain.UpdateJob{}, err
	}
	var job domain.UpdateJob
	if err := json.Unmarshal(data, &job); err != nil {
		return job, fmt.Errorf("decode update.json: %w", err)
	}
	if job.ID == "" || job.Phase == "" {
		return job, fmt.Errorf("update.json missing job id or phase")
	}
	return job, nil
}

func (s *UpdateStore) Save(ctx context.Context, job domain.UpdateJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.ID == "" || job.Phase == "" {
		return fmt.Errorf("update job missing id or phase")
	}
	job.ErrorMessage = sanitizeUpdateError(job.ErrorMessage)
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	job.UpdatedAt = now().UTC()
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update job: %w", err)
	}
	if err := writeAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save update job: %w", err)
	}
	if s.log != nil {
		s.log.Info("update phase recorded", slog.String("job", job.ID), slog.String("phase", job.Phase), slog.String("error_code", job.ErrorCode))
	}
	return nil
}

func (s *UpdateStore) Recover(ctx context.Context, workerAlive bool) (domain.UpdateJob, error) {
	job, err := s.Load(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return domain.UpdateJob{}, nil
	}
	if err != nil {
		return job, err
	}
	if !job.Terminal() && !workerAlive {
		job.Phase = "interrupted"
		job.ErrorCode = "worker_interrupted"
		job.ErrorMessage = "Update worker stopped before completion; inspect update.json and daemon.log."
		if err := s.Save(ctx, job); err != nil {
			return job, err
		}
		if s.log != nil {
			s.log.Warn("update worker interrupted", slog.String("job", job.ID))
		}
	}
	return job, nil
}

func sanitizeUpdateError(raw string) string {
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, line)
	if len(line) > 240 {
		line = line[:240]
	}
	return line
}
