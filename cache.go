package clone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cache keeps one persistent checkout per repository URL. A Cache must not be
// copied after its first use.
type Cache struct {
	Root  string // Parent directory for per-URL checkouts.
	Retry Retry  // Retry policy for clone and fetch operations.
	mu    sync.Map
}

// Dir returns the persistent directory for url under c.Root.
func (c *Cache) Dir(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.Root, hex.EncodeToString(sum[:]))
}

// Prepare updates the cache for url, replaces dst with a copy of the checkout,
// and returns its HEAD commit. dst must not overlap Root.
func (c *Cache) Prepare(ctx context.Context, url, ref, dst string) (string, error) {
	mutex := c.mutex(url)
	mutex.Lock()
	defer mutex.Unlock()

	overlap, err := pathsOverlap(c.Root, dst)
	if err != nil {
		return "", err
	}
	if overlap {
		return "", fmt.Errorf("destination %q overlaps cache root %q", dst, c.Root)
	}
	cacheDir := c.Dir(url)
	if err := os.MkdirAll(cacheDir, dirPerm); err != nil {
		return "", err
	}
	cacheSrc := filepath.Join(cacheDir, "src")
	if err := Ensure(ctx, c.Retry, url, cacheSrc, ref, false); err != nil {
		return "", err
	}
	commit := Head(ctx, cacheSrc)
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := CopyTree(cacheSrc, dst); err != nil {
		return "", fmt.Errorf("copy repository cache: %w", err)
	}
	return commit, nil
}

// EnsureCommit unshallows the cached checkout when commit is not already
// reachable. It does nothing when the checkout is absent or already complete.
func (c *Cache) EnsureCommit(ctx context.Context, url, commit string) error {
	if !ValidCommit(commit) {
		return fmt.Errorf("invalid commit %q", commit)
	}

	mutex := c.mutex(url)
	mutex.Lock()
	defer mutex.Unlock()

	cacheSrc := filepath.Join(c.Dir(url), "src")
	if _, err := os.Stat(filepath.Join(cacheSrc, ".git")); err != nil {
		return nil
	}
	policy := c.Retry.Resolved()
	if commitReachable(ctx, policy.Run, cacheSrc, commit) {
		return nil
	}
	out, _ := policy.Run(ctx, cacheSrc, nil, "rev-parse", "--is-shallow-repository")
	if strings.TrimSpace(out) != "true" {
		return nil
	}
	out, err := policy.Do(ctx, Command{
		Args:  []string{"-c", "credential.helper=", "fetch", "--unshallow", "--quiet", "origin"}, //nolint:goconst // Git argv is clearer with literal subcommands and flags.
		Label: "fetch",                                                                           //nolint:goconst // Retry notices use the literal Git subcommand.
		Dir:   cacheSrc,
		Env:   remoteEnv(),
	})
	if err != nil {
		return fmt.Errorf("unshallow %s: %s: %w", RedactURL(url), strings.TrimSpace(out), err)
	}
	return nil
}

// DiskBytes returns the number of bytes used by regular files in url's cache
// directory. It returns zero when the directory is absent.
func (c *Cache) DiskBytes(url string) int64 {
	size, _ := dirSize(c.Dir(url))
	return size
}

func (c *Cache) mutex(url string) *sync.Mutex {
	value, _ := c.mu.LoadOrStore(url, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func commitReachable(ctx context.Context, run Runner, dir, commit string) bool {
	_, err := run(ctx, dir, nil, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

func pathsOverlap(first, second string) (bool, error) {
	first, err := realAbs(first)
	if err != nil {
		return false, err
	}
	second, err = realAbs(second)
	if err != nil {
		return false, err
	}
	firstContainsSecond, err := pathContains(first, second)
	if err != nil {
		return false, err
	}
	secondContainsFirst, err := pathContains(second, first)
	if err != nil {
		return false, err
	}
	return firstContainsSecond || secondContainsFirst, nil
}

// realAbs resolves symlinks in the deepest existing ancestor of p and returns
// the absolute path with the not-yet-existing tail rejoined. filepath.Abs
// alone does not follow symlinks, so a symlinked ancestor of dst could
// otherwise pass pathsOverlap while pointing inside c.Root.
func realAbs(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}

func pathContains(parent, child string) (bool, error) {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}
