package clone

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyTree recursively copies src to dst, preserving permissions but not
// ownership or timestamps. Symlinks are recreated with their original target.
//
// The source is read through os.OpenRoot, so a file swapped for a symlink
// between listing and opening is refused at open time instead of reading
// bytes from outside the tree. Entries that are neither directories,
// symlinks nor regular files are refused: a FIFO would block on open and a
// device node would copy without bound. A cancelled context stops the walk
// at the next entry. src and dst may not contain one another, which would
// make the walk discover its own output.
func CopyTree(ctx context.Context, src, dst string) error {
	if containsPath(src, dst) || containsPath(dst, src) {
		return fmt.Errorf("copy source and destination overlap: %q and %q", src, dst)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := root.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFromRoot(root, name, target, info.Mode().Perm())
		default:
			return fmt.Errorf("refuse %s: not a regular file", name)
		}
	})
}

// containsPath reports whether child is parent or lies under it, comparing
// cleaned absolute paths without resolving symlinks.
func containsPath(parent, child string) bool {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	absChild, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absChild)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyFromRoot(root *os.Root, name, dst string, perm os.FileMode) error {
	in, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
