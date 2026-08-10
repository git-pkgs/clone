package clone

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/magic"
)

func seedBlobRepository(t testing.TB) (string, string) {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet", "-b", "main")
	files := map[string][]byte{
		"exact.txt":       []byte("12345"),
		"big.txt":         bytes.Repeat([]byte("a"), 128<<10),
		"binary":          {'a', 0, 'b'},
		"late-nul":        {'a', 'b', 'c', 0},
		"empty":           {},
		"png":             []byte("\x89PNG\r\n\x1a\n"),
		"invalid":         {0xff, 'a'},
		"utf16le":         {0xff, 0xfe, 'h', 0, 'i', 0},
		"nested/file.txt": []byte("nested"),
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, dir, "add", ".")
	runGitTest(t, dir, "commit", "--quiet", "-m", "files")
	return dir, runGitTest(t, dir, "rev-parse", "HEAD")
}

func BenchmarkBlob(b *testing.B) {
	dir, commit := seedBlobRepository(b)
	b.ReportAllocs()

	for b.Loop() {
		content, binary, truncated, err := Blob(context.Background(), dir, commit, "exact.txt", 5)
		if err != nil {
			b.Fatal(err)
		}
		if len(content) != 5 || binary || truncated {
			b.Fatal("unexpected Blob result")
		}
	}
}

func BenchmarkInspectBlob(b *testing.B) {
	dir, commit := seedBlobRepository(b)
	b.ReportAllocs()

	for b.Loop() {
		result, err := InspectBlob(context.Background(), dir, commit, "exact.txt", 5)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Content) != 5 || result.Detection.Kind != magic.KindText || result.Truncated {
			b.Fatal("unexpected InspectBlob result")
		}
	}
}

func TestInspectBlobClassifiesContent(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	tests := []struct {
		name          string
		path          string
		maxBytes      int64
		wantContent   []byte
		wantDetection magic.Result
		wantTruncated bool
	}{
		{
			name:        "complete text",
			path:        "exact.txt",
			maxBytes:    5,
			wantContent: []byte("12345"),
			wantDetection: magic.Result{
				Kind:     magic.KindText,
				MIME:     "text/plain",
				Format:   "text",
				Encoding: "utf-8",
			},
		},
		{
			name:          "truncated text",
			path:          "big.txt",
			maxBytes:      32,
			wantContent:   bytes.Repeat([]byte("a"), 32),
			wantTruncated: true,
			wantDetection: magic.Result{
				Kind:     magic.KindText,
				MIME:     "text/plain",
				Format:   "text",
				Encoding: "utf-8",
				Reason:   magic.ReasonNeedMore,
			},
		},
		{
			name:        "binary signature without NUL",
			path:        "png",
			maxBytes:    8,
			wantContent: []byte("\x89PNG\r\n\x1a\n"),
			wantDetection: magic.Result{
				Kind:   magic.KindBinary,
				MIME:   "image/png",
				Format: "png",
			},
		},
		{
			name:        "invalid UTF-8",
			path:        "invalid",
			maxBytes:    2,
			wantContent: []byte{0xff, 'a'},
			wantDetection: magic.Result{
				Kind:   magic.KindUnknown,
				Reason: magic.ReasonInvalidText,
			},
		},
		{
			name:        "UTF-16LE",
			path:        "utf16le",
			maxBytes:    6,
			wantContent: []byte{0xff, 0xfe, 'h', 0, 'i', 0},
			wantDetection: magic.Result{
				Kind:     magic.KindText,
				MIME:     "text/plain",
				Format:   "text",
				Encoding: "utf-16le",
			},
		},
		{
			name:          "early NUL",
			path:          "binary",
			maxBytes:      3,
			wantContent:   []byte{'a', 0, 'b'},
			wantDetection: magic.Result{Kind: magic.KindBinary},
		},
		{
			name:          "NUL beyond limit",
			path:          "late-nul",
			maxBytes:      3,
			wantContent:   []byte("abc"),
			wantTruncated: true,
			wantDetection: magic.Result{
				Kind:     magic.KindText,
				MIME:     "text/plain",
				Format:   "text",
				Encoding: "utf-8",
				Reason:   magic.ReasonNeedMore,
			},
		},
		{
			name:        "empty",
			path:        "empty",
			maxBytes:    0,
			wantContent: []byte{},
			wantDetection: magic.Result{
				Kind:   magic.KindText,
				MIME:   "text/plain",
				Format: "text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := InspectBlob(context.Background(), dir, commit, tt.path, tt.maxBytes)
			if err != nil {
				t.Fatalf("InspectBlob: %v", err)
			}
			if !bytes.Equal(result.Content, tt.wantContent) {
				t.Errorf("Content = %q, want %q", result.Content, tt.wantContent)
			}
			if result.Detection != tt.wantDetection {
				t.Errorf("Detection = %#v, want %#v", result.Detection, tt.wantDetection)
			}
			if result.Truncated != tt.wantTruncated {
				t.Errorf("Truncated = %v, want %v", result.Truncated, tt.wantTruncated)
			}
		})
	}
}

func TestInspectBlobReportsErrors(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	tests := []struct {
		name     string
		commit   string
		path     string
		maxBytes int64
		contains string
	}{
		{"missing path", commit, "missing.txt", 100, "does not exist"},
		{"invalid limit", commit, "exact.txt", -1, "non-negative"},
		{"invalid commit", "HEAD", "exact.txt", 100, "invalid commit"},
		{"invalid path", commit, "../exact.txt", 100, "invalid path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InspectBlob(context.Background(), dir, tt.commit, tt.path, tt.maxBytes)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error = %v, want error containing %q", err, tt.contains)
			}
		})
	}
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

func TestBlobReadsWithoutGitOnPath(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), dir, commit, "nested/file.txt", 6)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "nested" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobReadsPackedObject(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	runGitTest(t, dir, "gc", "--quiet", "--prune=now")
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), dir, commit, "big.txt", 32)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if !bytes.Equal(content, bytes.Repeat([]byte("a"), 32)) || binary || !truncated {
		t.Errorf("Blob = (%q, %v, %v), want truncated text", content, binary, truncated)
	}
}

func TestBlobReadsLinkedWorktree(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	worktree := filepath.Join(t.TempDir(), "checkout")
	runGitTest(t, dir, "worktree", "add", "--quiet", "--detach", worktree, commit)
	gitFile := filepath.Join(worktree, ".git")
	resolved, err := resolveGitFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(worktree, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: "+relative+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), worktree, commit, "exact.txt", 5)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "12345" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobResolvesAbbreviatedCommitFromSubdirectory(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	nested := filepath.Join(dir, "nested")
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), nested, commit[:7], "exact.txt", 5)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "12345" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobReadsBareRepositoryWithoutGitOnPath(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	bare := filepath.Join(t.TempDir(), "repo.git")
	runGitTest(t, dir, "clone", "--quiet", "--bare", dir, bare)
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), bare, commit, "exact.txt", 5)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "12345" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobPeelsAnnotatedTagWithoutGitOnPath(t *testing.T) {
	dir, _ := seedBlobRepository(t)
	runGitTest(t, dir, "tag", "-a", "blob-test", "-m", "blob test")
	tag := runGitTest(t, dir, "rev-parse", "blob-test^{tag}")
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), dir, tag, "exact.txt", 5)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if string(content) != "12345" || binary || truncated {
		t.Errorf("Blob = (%q, %v, %v), want exact text", content, binary, truncated)
	}
}

func TestBlobHonorsCanceledContext(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := Blob(ctx, dir, commit, "exact.txt", 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBlobReadsSHA256RepositoryWithGitFallback(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", "--object-format=sha256", "-b", "main")
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git does not support SHA-256 repositories: %s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "file.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "file")
	commit := runGitTest(t, dir, "rev-parse", "HEAD")
	if len(commit) != 64 {
		t.Fatalf("SHA-256 commit length = %d, want 64", len(commit))
	}

	for _, revision := range []string{commit, commit[:12]} {
		content, binary, truncated, err := Blob(context.Background(), dir, revision, "file.txt", 7)
		if err != nil {
			t.Fatalf("Blob(%q): %v", revision, err)
		}
		if string(content) != "content" || binary || truncated {
			t.Errorf("Blob(%q) = (%q, %v, %v), want exact text", revision, content, binary, truncated)
		}
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
