package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type quarantinedInboxFile struct {
	original   string
	quarantine string
}

func (i *Inbox) stageFile(dir string, data []byte, mode os.FileMode) (string, error) {
	if i.stageForSave != nil {
		return i.stageForSave(dir, data, mode)
	}
	return stageInboxFile(dir, data, mode)
}

func (i *Inbox) renameFile(old, new string) error {
	if i.renameForSave != nil {
		return i.renameForSave(old, new)
	}
	return os.Rename(old, new)
}

func (i *Inbox) quotaVictims(need int64, sharedOnly bool) ([]inboxFile, error) {
	limit := i.limit()
	quota := limit
	if sharedOnly {
		quota = i.SharedQuota()
	}
	if need > quota {
		return nil, fmt.Errorf("inbox: %d bytes exceed the %d byte quota: %w", need, quota, domain.ErrFileTooBig)
	}

	files, total, err := i.list()
	if err != nil {
		return nil, err
	}
	var shared int64
	for _, file := range files {
		if file.shared {
			shared += file.size
		}
	}
	if sharedOnly && total-shared+need > limit {
		return nil, fmt.Errorf("inbox: no room for %d shared bytes under the %d byte quota: %w", need, limit, domain.ErrFileTooBig)
	}

	var victims []inboxFile
	for _, file := range files {
		if total+need <= limit && (!sharedOnly || shared+need <= quota) {
			break
		}
		if sharedOnly && !file.shared {
			continue
		}
		victims = append(victims, file)
		total -= file.size
		if file.shared {
			shared -= file.size
		}
	}
	if total+need > limit || (sharedOnly && shared+need > quota) {
		return nil, fmt.Errorf("inbox: no room for %d bytes under the %d byte quota: %w", need, quota, domain.ErrFileTooBig)
	}
	return victims, nil
}

func stageInboxFile(dir string, data []byte, mode os.FileMode) (path string, err error) {
	file, err := os.CreateTemp(dir, ".inbox-stage-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create staged file: %w", err)
	}
	stagePath := file.Name()
	path = stagePath
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(stagePath)
		}
	}()
	if err = file.Chmod(mode); err != nil {
		return "", fmt.Errorf("chmod staged file: %w", err)
	}
	if _, err = file.Write(data); err != nil {
		return "", fmt.Errorf("write staged file: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("sync staged file: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close staged file: %w", err)
	}
	return path, nil
}

func (i *Inbox) quarantine(victims []inboxFile) ([]quarantinedInboxFile, error) {
	dir := filepath.Join(i.dir, inboxQuarantineDirName)
	if err := os.MkdirAll(dir, inboxDirMode); err != nil {
		return nil, fmt.Errorf("inbox: create quarantine directory: %w", err)
	}
	var moved []quarantinedInboxFile
	for _, victim := range victims {
		path := filepath.Join(dir, filepath.Base(victim.path))
		if _, err := os.Lstat(path); err == nil {
			return nil, errors.Join(fmt.Errorf("inbox: quarantine path already exists: %s", path), i.restoreQuarantined(moved))
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(fmt.Errorf("inbox: inspect quarantine path %s: %w", path, err), i.restoreQuarantined(moved))
		}
		if err := i.renameFile(victim.path, path); err != nil {
			return nil, errors.Join(fmt.Errorf("inbox: quarantine %s: %w", victim.path, err), i.restoreQuarantined(moved))
		}
		moved = append(moved, quarantinedInboxFile{original: victim.path, quarantine: path})
	}
	return moved, nil
}

func (i *Inbox) restoreQuarantined(files []quarantinedInboxFile) error {
	var failures []error
	for n := len(files) - 1; n >= 0; n-- {
		file := files[n]
		if err := i.renameFile(file.quarantine, file.original); err != nil {
			i.log.Error("inbox quota rollback failed", "original", file.original, "quarantine", file.quarantine, "err", err)
			failures = append(failures, fmt.Errorf("restore %s from %s: %w", file.original, file.quarantine, err))
		}
	}
	return errors.Join(failures...)
}

// recoverQuarantined restores data left by an interrupted quota transaction.
// The original basename is part of the deterministic quarantine path.
func (i *Inbox) recoverQuarantined() error {
	entries, err := os.ReadDir(i.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list inbox for quota recovery: %w", err)
	}
	var failures []error
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == inboxQuarantineDirName {
			quarantined, readErr := os.ReadDir(filepath.Join(i.dir, inboxQuarantineDirName))
			if readErr != nil {
				failures = append(failures, fmt.Errorf("list inbox quarantine directory: %w", readErr))
				continue
			}
			for _, old := range quarantined {
				if !old.Type().IsRegular() {
					continue
				}
				if unsafeInboxName(old.Name()) {
					failures = append(failures, fmt.Errorf("invalid quarantined inbox name %q", old.Name()))
					continue
				}
				if err := i.restoreQuarantinedName(filepath.Join(i.dir, inboxQuarantineDirName, old.Name()), old.Name()); err != nil {
					failures = append(failures, err)
				}
			}
			continue
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), inboxQuarantinePrefix) {
			continue
		}
		name := strings.TrimPrefix(entry.Name(), inboxQuarantinePrefix)
		if name == "" || unsafeInboxName(name) {
			failures = append(failures, fmt.Errorf("invalid quarantined inbox name %q", entry.Name()))
			continue
		}
		if err := i.restoreQuarantinedName(filepath.Join(i.dir, entry.Name()), name); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (i *Inbox) restoreQuarantinedName(quarantine, name string) error {
	original := filepath.Join(i.dir, name)
	if _, err := os.Lstat(original); err == nil {
		return fmt.Errorf("cannot restore quarantined inbox file %s: destination exists", quarantine)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect restore destination %s: %w", original, err)
	}
	if err := os.Rename(quarantine, original); err != nil {
		return fmt.Errorf("restore quarantined inbox file %s: %w", original, err)
	}
	i.log.Warn("restored inbox file from interrupted quota transaction", "path", original)
	return nil
}
