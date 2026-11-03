package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopyTreeCopiesFilesDirectoriesAndSymlinks(t *testing.T) {
	src := t.TempDir()
	nested := filepath.Join(src, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(nested, "file.txt")
	if err := os.WriteFile(sourceFile, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("nested", "file.txt"), filepath.Join(src, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating a symlink needs privileges on windows: %v", err)
		}
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyTree(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dst, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "content" {
		t.Errorf("content = %q", content)
	}
	info, err := os.Stat(filepath.Join(dst, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 600", info.Mode().Perm())
	}
	link, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if link != filepath.Join("nested", "file.txt") {
		t.Errorf("link target = %q", link)
	}
}

func TestCopyTreeReturnsSourceError(t *testing.T) {
	err := CopyTree(context.Background(), filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "copy"))
	if err == nil {
		t.Fatal("CopyTree succeeded for missing source")
	}
}

// src or dst containing the other would make the walk discover its own
// output, so both directions are refused before anything is read.
func TestCopyTreeRefusesOverlap(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := CopyTree(ctx, src, filepath.Join(src, "nested", "out")); err == nil {
		t.Fatal("CopyTree succeeded with dst inside src")
	}
	if err := CopyTree(ctx, filepath.Join(t.TempDir(), "in"), t.TempDir()); err == nil {
		t.Fatal("CopyTree succeeded with src inside dst")
	}
}

// A cancelled context stops the walk at the next entry.
func TestCopyTreeHonoursContext(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := CopyTree(ctx, src, filepath.Join(t.TempDir(), "copy"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyTree error = %v, want context.Canceled", err)
	}
}
