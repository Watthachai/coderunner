package buildstep

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SnapshotMigrations captures the committed upgrade history before an edit.
// Existing customer migrations are immutable; new migrations may be appended.
func SnapshotMigrations(dir string) (map[string][]byte, error) {
	history := make(map[string][]byte)
	root := filepath.Join(dir, "prisma", "migrations")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration history contains a symlink")
		}
		if !strings.HasSuffix(path, ".sql") && entry.Name() != "migration_lock.toml" {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		history[relative] = data
		return nil
	})
	return history, err
}

func CheckMigrationHistory(dir string, before map[string][]byte) error {
	for name, data := range before {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(data, after) {
			return fmt.Errorf("existing migration %s was removed or rewritten; preserve it and append a new migration", name)
		}
	}
	return nil
}
