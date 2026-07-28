package clone

import (
	"os"
	"path/filepath"
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
	if err := CopyTree(src, dst); err != nil {
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
	if info.Mode().Perm() != 0o600 {
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
	err := CopyTree(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "copy"))
	if err == nil {
		t.Fatal("CopyTree succeeded for missing source")
	}
}
