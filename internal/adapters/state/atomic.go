// Package state persists the plugin's JSON files under the Herdr config and
// state directories: config.json, mapping.json and the daemon pid file.
package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic writes data to path through a temp file in the same directory
// so readers never see a partial file: create, write, fsync, chmod, rename.
func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", name, path, err)
	}
	return nil
}
