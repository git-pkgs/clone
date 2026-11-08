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

func TestCopyTreeSingleFileSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("body"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "note-copy.txt")
	if err := CopyTree(context.Background(), src, dst); err != nil {
		t.Fatalf("CopyTree on a regular file failed: %v", err)
	}
	content, err := os.ReadFile(dst)
	if err != nil || string(content) != "body" {
		t.Fatalf("dst content = %q, %v; want %q", content, err, "body")
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Errorf("dst mode = %v, want %v", info.Mode().Perm(), os.FileMode(0o640))
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

func TestCopyTreeRefusesSymlinkPlantedUnderDestinationMidCopy(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	sub := filepath.Join(src, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	esc := filepath.Join(base, "escape")
	if err := os.Mkdir(esc, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dst, ".probe")
	if err := os.Symlink(esc, probe); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	opened := false
	open := func(root *os.Root, name string) (*os.File, error) {
		f, err := openRootDir(root, name)
		if err == nil && name == "sub" && !opened {
			opened = true
			// Plant a link out of dst where Mkdir would create it: by path,
			// the Mkdir and file create below would land in esc.
			if lerr := os.Symlink(esc, filepath.Join(dst, "sub")); lerr != nil {
				t.Fatal(lerr)
			}
		}
		return f, err
	}
	if err := copyTree(context.Background(), src, dst, open); err == nil {
		t.Fatal("copyTree succeeded after a symlink was planted under dst")
	}
	entries, err := os.ReadDir(esc)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("escape directory holds %d entries; nothing may be written outside dst", len(entries))
	}
	content, err := os.ReadFile(filepath.Join(src, "sub", "file.txt"))
	if err != nil || string(content) != "content" {
		t.Fatalf("source changed outside dst: %q, %v", content, err)
	}
}
