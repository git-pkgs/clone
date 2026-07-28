package clone

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedBlobRepository(t *testing.T) (string, string) {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet", "-b", "main")
	files := map[string][]byte{
		"exact.txt": []byte("12345"),
		"big.txt":   bytes.Repeat([]byte("a"), 128<<10),
		"binary":    {'a', 0, 'b'},
		"late-nul":  {'a', 'b', 'c', 0},
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, dir, "add", ".")
	runGitTest(t, dir, "commit", "--quiet", "-m", "files")
	return dir, runGitTest(t, dir, "rev-parse", "HEAD")
}

func TestBlobReadsTextAtLimit(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	content, binary, truncated, err := Blob(context.Background(), dir, commit, "exact.txt", 5)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "12345" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobCapsAndDrainsLargeOutput(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	content, binary, truncated, err := Blob(context.Background(), dir, commit, "big.txt", 32)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if len(content) != 32 || binary || !truncated {
		t.Errorf("Blob returned len=%d binary=%v truncated=%v", len(content), binary, truncated)
	}
	if !bytes.Equal(content, bytes.Repeat([]byte("a"), 32)) {
		t.Errorf("content = %q", content)
	}
}

func TestBlobDetectsNULWithinReturnedRange(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	content, binary, truncated, err := Blob(context.Background(), dir, commit, "binary", 3)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if content != nil || !binary || truncated {
		t.Errorf("Blob = (%v, %v, %v), want nil, binary, complete", content, binary, truncated)
	}
}

func TestBlobIgnoresNULPastLimit(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	content, binary, truncated, err := Blob(context.Background(), dir, commit, "late-nul", 3)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "abc" || binary || !truncated {
		t.Errorf("Blob = (%q, %v, %v), want truncated text", content, binary, truncated)
	}
}

func TestBlobReportsGitError(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	_, _, _, err := Blob(context.Background(), dir, commit, "missing.txt", 100)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v, want missing path error", err)
	}
}

func TestBlobRejectsInvalidLimit(t *testing.T) {
	_, _, _, err := Blob(context.Background(), t.TempDir(), "deadbeef", "file", -1)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %v", err)
	}
}

func TestBlobRejectsInvalidCommitAndPath(t *testing.T) {
	// The reason Blob validates internally rather than trusting the caller:
	// git show --output=/path:x writes HEAD's log to /path:x. Without the
	// ValidCommit gate and --end-of-options, an unvalidated commit reaching
	// argv is arbitrary-path file write.
	dir := t.TempDir()
	for _, c := range []string{"--output=/tmp/x", "HEAD", "abcg", ""} {
		if _, _, _, err := Blob(context.Background(), dir, c, "file", 100); err == nil {
			t.Errorf("Blob(commit=%q) should reject invalid commit", c)
		}
	}
	for _, p := range []string{"../etc/passwd", "/abs", "", "x\x00y"} {
		if _, _, _, err := Blob(context.Background(), dir, "deadbeef", p, 100); err == nil {
			t.Errorf("Blob(path=%q) should reject invalid path", p)
		}
	}
}
