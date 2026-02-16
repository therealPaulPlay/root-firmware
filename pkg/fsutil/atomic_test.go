package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWrite_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	data := []byte("hello, world")

	err := AtomicWrite(path, data, 0644)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(content, data) {
		t.Errorf("file content = %q, want %q", content, data)
	}
}

func TestAtomicWrite_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := AtomicWrite(path, []byte("data"), 0600)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestAtomicWrite_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	AtomicWrite(path, []byte("initial"), 0644)

	newData := []byte("updated content")
	err := AtomicWrite(path, newData, 0644)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	content, _ := os.ReadFile(path)
	if !bytes.Equal(content, newData) {
		t.Errorf("file content = %q, want %q", content, newData)
	}
}

func TestAtomicWrite_EmptyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	err := AtomicWrite(path, []byte{}, 0644)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	content, _ := os.ReadFile(path)
	if len(content) != 0 {
		t.Errorf("file content = %q, want empty", content)
	}
}

func TestAtomicWrite_InvalidDirectory(t *testing.T) {
	path := "/nonexistent/directory/file.txt"

	err := AtomicWrite(path, []byte("data"), 0644)
	if err == nil {
		t.Error("AtomicWrite() should error for non-existent directory")
	}
}

func TestAtomicWrite_NoTempFileLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	AtomicWrite(path, []byte("data"), 0644)

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("temp file not cleaned up: %s", entry.Name())
		}
	}
}

func TestAtomicWrite_NoTempFileLeftOnError(t *testing.T) {
	dir := t.TempDir()
	// Target is a directory, so rename will fail
	targetDir := filepath.Join(dir, "target")
	os.Mkdir(targetDir, 0755)

	AtomicWrite(targetDir, []byte("data"), 0644)

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("temp file not cleaned up after error: %s", entry.Name())
		}
	}
}

func TestAtomicWrite_OriginalUnchangedOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "original.txt")

	// Make path a directory to cause rename to fail
	os.Mkdir(path, 0755)

	// Try to write - this should fail because we can't rename over a directory
	err := AtomicWrite(path, []byte("new content"), 0644)
	if err == nil {
		t.Fatal("AtomicWrite() should error when target is a directory")
	}

	// Verify directory still exists (wasn't corrupted)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("original path was removed: %v", err)
	}
	if !info.IsDir() {
		t.Error("original directory was replaced")
	}
}

