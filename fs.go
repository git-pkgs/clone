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
// ownership or timestamps. src may be a regular file or symlink; non-regular
// entries are refused, src and dst may not overlap, and dst must not already
// contain src's subdirectories.
func CopyTree(ctx context.Context, src, dst string) error {
	return copyTree(ctx, src, dst, openRootDir)
}

// copyTree takes the directory opener as a parameter so tests can interpose on
// a path between its parent being listed and the open.
func copyTree(ctx context.Context, src, dst string, open func(*os.Root, string) (*os.File, error)) error {
	if srcInfo, err := os.Lstat(src); err == nil {
		if srcInfo.Mode().IsRegular() {
			return copyFileTree(ctx, src, dst)
		}
		if srcInfo.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(src)
			if err != nil {
				return err
			}
			return os.Symlink(link, dst)
		}
	}
	if containsPath(src, dst) || containsPath(dst, src) {
		return fmt.Errorf("copy source and destination overlap: %q and %q", src, dst)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	probe, err := open(root, ".")
	if err != nil {
		return err
	}
	info, err := probe.Stat()
	_ = probe.Close()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()
	return copyDir(ctx, root, ".", dstRoot, ".", false, open)
}

func copyDir(ctx context.Context, root *os.Root, name string, dstRoot *os.Root, rel string, mkdir bool, open func(*os.Root, string) (*os.File, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := open(root, name)
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	if mkdir {
		if err := dstRoot.Mkdir(rel, info.Mode().Perm()); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := filepath.Join(name, entry.Name())
		target := filepath.Join(rel, entry.Name())
		switch {
		case entry.IsDir():
			if err := copyDir(ctx, root, child, dstRoot, target, true, open); err != nil {
				return err
			}
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := root.Readlink(child)
			if err != nil {
				return err
			}
			if err := dstRoot.Symlink(link, target); err != nil {
				return err
			}
		default:
			if !entry.Type().IsRegular() {
				return fmt.Errorf("refuse %s: not a regular file", child)
			}
			if err := copyFromRoot(root, child, dstRoot, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// openRootDir opens name without blocking on a FIFO and verifies through the
// open descriptor that it is still a directory before it is enumerated.
func openRootDir(root *os.Root, name string) (*os.File, error) {
	dir, err := root.OpenFile(name, os.O_RDONLY|nonblockFlag, 0)
	if err != nil {
		return nil, err
	}
	info, err := dir.Stat()
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = dir.Close()
		return nil, fmt.Errorf("refuse %s: not a directory", name)
	}
	return dir, nil
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

// copyFileTree copies a lone regular file src with copyFromRoot's
// open-then-stat refusal.
func copyFileTree(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := os.OpenFile(src, os.O_RDONLY|nonblockFlag, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse %s: not a regular file", src)
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

// copyFromRoot opens name without blocking on a FIFO, then stats the open
// descriptor so whatever is there now decides whether it is copied.
func copyFromRoot(root *os.Root, name string, dstRoot *os.Root, rel string) error {
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

	out, err := dstRoot.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
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
