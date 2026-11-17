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

// clearWorktree removes everything in dst except .git, so the reset that
// follows rebuilds the working tree and no submodule keeps files from an
// earlier checkout. Submodule repositories under .git/modules are kept, so
// the update checks them out again without fetching what they already hold.
func clearWorktree(dst string) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("clear worktree: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return fmt.Errorf("clear worktree: %w", err)
		}
	}
	return nil
}

// syncModuleRemotes points each existing submodule repository under dir's
// .git/modules at the URL sync recorded for it. Sync updates a repository
// only through its checkout, which clearWorktree removed, and update would
// otherwise fetch from the URL an earlier commit declared.
func syncModuleRemotes(ctx context.Context, policy Retry, dir string, links []gitlink) error {
	for _, link := range links {
		gitDir, err := moduleGitDir(ctx, policy, dir, link.name)
		if err != nil {
			return err
		}
		if gitDir == "" || !isGitDir(gitDir) {
			continue
		}
		url := submoduleURLConfig(ctx, policy, dir, link.name)
		if url == "" {
			continue
		}
		if _, err := policy.Run(ctx, "", nil, "config", "--file", filepath.Join(gitDir, "config"), "remote.origin.url", url); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
	}
	return nil
}

// moduleGitDir returns where dir's repository keeps the submodule named name,
// or "" when that cannot be resolved. With extensions.submodulePathConfig Git
// records the location, relative to the checkout, in submodule.<name>.gitdir
// rather than using modules/<name> under the Git directory.
func moduleGitDir(ctx context.Context, policy Retry, dir, name string) (string, error) {
	if out, err := policy.Run(ctx, dir, nil, "config", "-z", "--get", "submodule."+name+".gitdir"); err == nil {
		location := strings.TrimSuffix(out, "\x00")
		if !filepath.IsAbs(location) {
			location = filepath.Join(dir, location)
		}
		return location, nil
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	out, err := policy.Run(ctx, dir, nil, "rev-parse", "--git-path", "modules/"+name)
	if err != nil {
		return "", ctx.Err()
	}
	path := strings.TrimRight(out, "\r\n")
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path, nil
}

func isGitDir(path string) bool {
	for _, name := range []string{"HEAD", "config"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// emptySubmodule replaces the directory at the slash path rel with an empty
// one, as an uninitialized submodule appears. The submodule's repository
// under .git/modules is kept for a later update.
func emptySubmodule(root *os.Root, rel string) error {
	info, err := lstatBelow(root, rel)
	if err != nil || info == nil {
		return err
	}
	name := filepath.FromSlash(rel)
	if info.IsDir() {
		dir, err := root.Open(name)
		if err != nil {
			return fmt.Errorf("remove stale submodule %s: %w", rel, err)
		}
		_, err = dir.ReadDir(1)
		_ = dir.Close()
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
	if err := root.RemoveAll(name); err != nil {
		return fmt.Errorf("remove stale submodule %s: %w", rel, err)
	}
	if err := root.Mkdir(name, dirPerm); err != nil {
		return fmt.Errorf("remove stale submodule %s: %w", rel, err)
	}
	return nil
}

// lstatBelow returns the entry at the slash path rel, or nil when it is
// absent or reached through anything but real directories. os.Root follows a
// symlink that stays inside the root, which could otherwise point a removal
// at .git.
func lstatBelow(root *os.Root, rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i], "/")))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("inspect stale submodule %s: %w", rel, err)
		}
		if i == len(parts) {
			return info, nil
		}
		if !info.IsDir() {
			return nil, nil
		}
	}
	return nil, nil
}
