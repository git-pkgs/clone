package clone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyTree recursively copies src to dst, preserving permissions but not
// ownership or timestamps. Symlinks are recreated with their original target.
// Entries that are neither directories, symlinks nor regular files are refused,
// and src and dst may not contain one another.
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
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch {
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			link, err := root.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFromRoot(root, name, target)
		}
	})
}

// containsPath reports whether child is parent or lies under it, resolving
// symlinks in both so a link into the other tree still counts as overlap.
func containsPath(parent, child string) bool {
	rel, err := filepath.Rel(resolveExistingPrefix(parent), resolveExistingPrefix(child))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExistingPrefix returns the deepest existing ancestor of path with its
// symlinks resolved, with the missing tail appended unchanged.
func resolveExistingPrefix(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	rest := ""
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			return filepath.Join(resolved, rest)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return filepath.Join(abs, rest)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return filepath.Join(abs, rest)
		}
		rest = filepath.Join(filepath.Base(abs), rest)
		abs = parent
	}
}

// copyFromRoot opens name without following a swapped-in FIFO, then stats the
// open descriptor: whatever sits there now decides whether it is copied.
func copyFromRoot(root *os.Root, name, dst string) error {
	in, err := root.OpenFile(name, os.O_RDONLY|nonblockFlag, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse %s: not a regular file", name)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
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
