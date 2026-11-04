package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestCopyTreeRefusesOverlap(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := CopyTree(ctx, src, filepath.Join(src, "nested", "out")); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("CopyTree error = %v, want an overlap refusal for dst inside src", err)
	}

	base := t.TempDir()
	dst := filepath.Join(base, "out")
	inside := filepath.Join(dst, "in")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(ctx, inside, dst); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("CopyTree error = %v, want an overlap refusal for src inside dst", err)
	}
}

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
