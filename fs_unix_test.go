//go:build unix

package clone

import (
	"context"
	"errors"
	"io/fs"
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

func TestCopyTreeRecreatesSymlinkSource(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "note.txt")
	if err := os.WriteFile(file, []byte("body"), 0o640); err != nil {
		t.Fatal(err)
	}
	checkLink := func(name, src, dst, target string) {
		t.Helper()
		if err := CopyTree(context.Background(), src, dst); err != nil {
			t.Fatalf("CopyTree on a %s symlink source failed: %v", name, err)
		}
		info, err := os.Lstat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("dst for a %s symlink source has mode %v, want a recreated symlink", name, info.Mode())
		}
		link, err := os.Readlink(dst)
		if err != nil || link != target {
			t.Fatalf("dst link = %q, %v; want %q", link, err, target)
		}
	}
	cases := []struct{ name, target string }{
		{"to-file", file},
		{"to-dir", base},
		{"dangling", filepath.Join(base, "gone")},
	}
	for _, tc := range cases {
		src := filepath.Join(t.TempDir(), tc.name)
		if err := os.Symlink(tc.target, src); err != nil {
			t.Fatal(err)
		}
		checkLink(tc.name, src, filepath.Join(t.TempDir(), "dst"), tc.target)
	}

	// A link whose target is an ancestor of dst: only the link is recreated and
	// nothing is walked, so the overlap check must not resolve it and refuse it.
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tree, "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tree, "b", "up")
	if err := os.Symlink("..", src); err != nil {
		t.Fatal(err)
	}
	checkLink("ancestor-target", src, filepath.Join(tree, "c", "up"), "..")
}
