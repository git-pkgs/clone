//go:build !windows

package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestCopyTreeRefusesDirectoryReplacedByFIFO(t *testing.T) {
	src := t.TempDir()
	sub := filepath.Join(src, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(root *os.Root, name string) (*os.File, error) {
		if name == "sub" {
			if err := os.RemoveAll(sub); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(sub, 0o600); err != nil {
				return nil, err
			}
		}
		return openRootDir(root, name)
	}
	done := make(chan error, 1)
	go func() {
		done <- copyTree(context.Background(), src, filepath.Join(t.TempDir(), "copy"), open)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("CopyTree error = %v, want a not-a-directory refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CopyTree blocked on a directory replaced by a FIFO")
	}
}

func TestCopyTreeHonoursCancellationDuringWalk(t *testing.T) {
	src := t.TempDir()
	sub := filepath.Join(src, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func(root *os.Root, name string) (*os.File, error) {
		if name == "sub" {
			cancel()
		}
		return openRootDir(root, name)
	}
	err := copyTree(ctx, src, filepath.Join(t.TempDir(), "copy"), open)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyTree error = %v, want context.Canceled", err)
	}
}
