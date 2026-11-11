package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Backup writes a consistent snapshot of the index to path with SQLite's
// online VACUUM INTO. The source may be open for reading (and another process
// may be writing); the destination must not already exist, because SQLite
// refuses to overwrite a database file and a backup should never clobber one.
//
// VACUUM INTO creates a freshly packed database rather than copying the WAL,
// so the result is a single self-contained file with no -wal/-shm siblings.
func (s *Store) Backup(path string) error {
	if path == "" {
		return errors.New("backup destination is empty")
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("backup destination is a directory: %s", path)
		}
		return fmt.Errorf("backup destination already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create backup directory %s: %w", dir, err)
		}
	}
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat backup %s: %w", path, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("backup %s is empty", path)
	}
	return nil
}
