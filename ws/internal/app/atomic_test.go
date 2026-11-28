package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicFileCreatesReplacesAndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "secret.conf")
	if err := WriteAtomicFile(target, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("WriteAtomicFile create: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "first\n" {
		t.Fatalf("unexpected content %q err=%v", string(got), err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 mode, got %v err=%v", fi.Mode().Perm(), err)
	}

	if err := WriteAtomicFile(target, []byte("second\n"), 0o644); err != nil {
		t.Fatalf("WriteAtomicFile replace: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "second\n" {
		t.Fatalf("unexpected replaced content %q err=%v", string(got), err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("expected 0644 mode, got %v err=%v", fi.Mode().Perm(), err)
	}

	realFile := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(realFile, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(realFile, linkPath); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomicFile(linkPath, []byte("overwrite\n"), 0o600); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	realGot, err := os.ReadFile(realFile)
	if err != nil || string(realGot) != "untouched\n" {
		t.Fatalf("expected symlink target untouched, got %q err=%v", string(realGot), err)
	}
}
