package buildstep

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationHistoryAllowsAppendButRejectsRewriteAndRemoval(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "prisma/migrations/001_init/migration.sql")
	if err := os.MkdirAll(filepath.Dir(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("CREATE TABLE customer(id integer);"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	appended := filepath.Join(dir, "prisma/migrations/002_add/migration.sql")
	if err := os.MkdirAll(filepath.Dir(appended), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appended, []byte("ALTER TABLE customer ADD name text;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckMigrationHistory(dir, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("CREATE TABLE replacement(id integer);"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckMigrationHistory(dir, snapshot); err == nil {
		t.Fatal("rewritten migration accepted")
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := CheckMigrationHistory(dir, snapshot); err == nil {
		t.Fatal("removed migration accepted")
	}
}
