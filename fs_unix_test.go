//go:build !windows

package clone

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopyTreeRefusesNonRegularFiles(t *testing.T) {
	src := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CopyTree(context.Background(), src, filepath.Join(t.TempDir(), "copy"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("CopyTree error = %v, want a non-regular-file refusal", err)
	}
}

func TestCopyTreeRefusesOverlapThroughASymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	err := CopyTree(context.Background(), src, filepath.Join(link, "out"))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("CopyTree error = %v, want an overlap refusal through the symlink", err)
	}
}
