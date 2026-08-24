package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheDirIsStableAndSeparatedByURL(t *testing.T) {
	cache := Cache{Root: "/data/cache"}
	first := cache.Dir("https://example.com/a")
	if first != cache.Dir("https://example.com/a") {
		t.Fatal("same URL produced different cache directories")
	}
	if first == cache.Dir("https://example.com/b") {
		t.Fatal("different URLs produced the same cache directory")
	}
	prefix := filepath.Clean(cache.Root) + string(filepath.Separator)
	if !strings.HasPrefix(first, prefix) {
		t.Errorf("cache path %q is not under %q", first, cache.Root)
	}
}

func TestCachePrepareUpdatesAndReplacesDestination(t *testing.T) {
	origin := newOriginFixture(t)
	cache := Cache{Root: t.TempDir()}
	dst := filepath.Join(t.TempDir(), "workspace", "src")

	commit, err := cache.Prepare(context.Background(), origin.url, "", dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if commit != origin.mainSHA {
		t.Errorf("commit = %q, want %q", commit, origin.mainSHA)
	}
	content, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "main\n" {
		t.Errorf("content = %q", content)
	}

	if err := os.WriteFile(filepath.Join(dst, "generated"), []byte("remove me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin.dir, "file.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, origin.dir, "commit", "--quiet", "-am", "updated")
	wantCommit := runGitTest(t, origin.dir, "rev-parse", "HEAD")

	commit, err = cache.Prepare(context.Background(), origin.url, "", dst)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if commit != wantCommit {
		t.Errorf("second commit = %q, want %q", commit, wantCommit)
	}
	if _, err := os.Stat(filepath.Join(dst, "generated")); !os.IsNotExist(err) {
		t.Errorf("stale destination file remains: %v", err)
	}
	content, err = os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "updated\n" {
		t.Errorf("updated content = %q", content)
	}
	if cache.DiskBytes(origin.url) == 0 {
		t.Error("DiskBytes = 0 after Prepare")
	}
}

func TestCachePrepareIncludesSubmodules(t *testing.T) {
	fixture := newSubmoduleOriginFixture(t)
	cache := Cache{Root: t.TempDir(), RecurseSubmodules: true}
	dst := filepath.Join(t.TempDir(), "workspace", "src")

	if _, err := cache.Prepare(context.Background(), fixture.origin.url, "", dst); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dst, "vendor", "library", "vendor.c"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\n" {
		t.Errorf("submodule content = %q, want first revision", content)
	}
}

func TestCachePrepareRejectsDestinationOverlappingCache(t *testing.T) {
	rootParent := t.TempDir()
	cache := Cache{Root: filepath.Join(rootParent, "cache")}
	url := "https://example.invalid/repo"
	for _, dst := range []string{
		rootParent,
		cache.Root,
		cache.Dir(url),
		filepath.Join(cache.Dir(url), "workspace"),
		cache.Dir("https://example.invalid/other"),
	} {
		_, err := cache.Prepare(context.Background(), url, "", dst)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Errorf("Prepare destination %q error = %v, want overlap error", dst, err)
		}
	}
}

func TestCacheEnsureCommitUnshallowsHistory(t *testing.T) {
	origin := newOriginFixture(t)
	cache := Cache{Root: t.TempDir()}
	if _, err := cache.Prepare(context.Background(), origin.url, "", filepath.Join(t.TempDir(), "src")); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	cacheSrc := filepath.Join(cache.Dir(origin.url), "src")
	if got := runGitTest(t, cacheSrc, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("cache shallow = %q, want true", got)
	}
	cmdErr := func() error {
		_, err := Run(context.Background(), cacheSrc, nil, "cat-file", "-e", origin.firstSHA+"^{commit}")
		return err
	}()
	if cmdErr == nil {
		t.Fatal("historical commit is already reachable in shallow cache")
	}

	if err := cache.EnsureCommit(context.Background(), origin.url, origin.firstSHA); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}
	if got := runGitTest(t, cacheSrc, "rev-parse", "--is-shallow-repository"); got != "false" {
		t.Errorf("cache shallow = %q, want false", got)
	}
	runGitTest(t, cacheSrc, "cat-file", "-e", origin.firstSHA+"^{commit}")
}

func TestCacheEnsureCommitNoCacheIsNoOp(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	if err := cache.EnsureCommit(context.Background(), "https://example.com/repo", "deadbeef"); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}
}

func TestCacheEnsureCommitRejectsInvalidCommit(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	err := cache.EnsureCommit(context.Background(), "https://example.com/repo", "--help")
	if err == nil || !strings.Contains(err.Error(), "invalid commit") {
		t.Fatalf("error = %v", err)
	}
}

func TestCacheEnsureCommitRetriesUnshallowFetch(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	url := "https://example.invalid/repo"
	cacheSrc := filepath.Join(cache.Dir(url), "src")
	if err := os.MkdirAll(filepath.Join(cacheSrc, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	fetchCalls := 0
	cache.Retry = Retry{
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			switch subcommand(args) {
			case "cat-file":
				return "", errors.New("missing object")
			case "rev-parse":
				return "true\n", nil
			case "fetch":
				fetchCalls++
				if fetchCalls == 1 {
					return "fatal: Connection reset by peer", errGitExit
				}
				return "", nil
			default:
				return "", errors.New("unexpected Git command")
			}
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}

	if err := cache.EnsureCommit(context.Background(), url, "deadbeef"); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}
	if fetchCalls != 2 {
		t.Errorf("fetch calls = %d, want 2", fetchCalls)
	}
}

func TestCacheEnsureCommitSkipsUnreachableCommitInFullClone(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	url := "https://example.invalid/repo"
	cacheSrc := filepath.Join(cache.Dir(url), "src")
	if err := os.MkdirAll(filepath.Join(cacheSrc, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache.Retry = Retry{
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			switch args[0] {
			case "cat-file":
				return "", errors.New("missing object")
			case "rev-parse":
				return "false\n", nil
			case "fetch":
				t.Fatal("full clone must not fetch")
			}
			return "", nil
		},
	}
	if err := cache.EnsureCommit(context.Background(), url, "deadbeef"); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}
}

func TestCacheDiskBytesCountsRegularFiles(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	url := "https://example.com/repo"
	dir := cache.Dir(url)
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "two"), make([]byte, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if got := cache.DiskBytes(url); got != 30 {
		t.Errorf("DiskBytes = %d, want 30", got)
	}
	if got := cache.DiskBytes("https://example.com/missing"); got != 0 {
		t.Errorf("missing DiskBytes = %d, want 0", got)
	}
}
