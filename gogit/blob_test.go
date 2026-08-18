package gogit

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
		"empty":           {},
		"png":             []byte("\x89PNG\r\n\x1a\n"),
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

func TestInspectBlobClassifiesContentWithoutGitOnPath(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	t.Setenv("PATH", t.TempDir())

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
			name:        "binary signature",
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

func TestInspectBlobRejectsInvalidInput(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	tests := []struct {
		name     string
		commit   string
		path     string
		maxBytes int64
		contains string
	}{
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

func TestBlobReadsPackedObjectWithoutGitOnPath(t *testing.T) {
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

func TestBlobReadsLinkedWorktreeWithoutGitOnPath(t *testing.T) {
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

func TestBlobPreservesGoGitAndGitErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())

	_, _, _, err := Blob(context.Background(), dir, strings.Repeat("a", 40), "file.txt", 5)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error = %v, want exec.ErrNotFound", err)
	}
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %v, want starting path %q", err, dir)
	}
}

func TestBlobAPIsFallBackForSHA256Repository(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dir, "binary"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "file.txt", "binary")
	runGitTest(t, dir, "commit", "--quiet", "-m", "file")
	commit := runGitTest(t, dir, "rev-parse", "HEAD")

	for _, revision := range []string{commit, commit[:12]} {
		content, binary, truncated, err := Blob(context.Background(), dir, revision, "file.txt", 7)
		if err != nil {
			t.Fatalf("Blob(%q): %v", revision, err)
		}
		if string(content) != "content" || binary || truncated {
			t.Errorf("Blob(%q) = (%q, %v, %v), want exact text", revision, content, binary, truncated)
		}

		content, binary, truncated, err = Blob(context.Background(), dir, revision, "binary", 3)
		if err != nil {
			t.Fatalf("Blob(%q, binary): %v", revision, err)
		}
		if content != nil || !binary || truncated {
			t.Errorf("Blob(%q, binary) = (%q, %v, %v), want complete binary", revision, content, binary, truncated)
		}

		result, inspectErr := InspectBlob(context.Background(), dir, revision, "binary", 3)
		if inspectErr != nil {
			t.Fatalf("InspectBlob(%q): %v", revision, inspectErr)
		}
		if !bytes.Equal(result.Content, []byte{'a', 0, 'b'}) || result.Detection.Kind != magic.KindBinary || result.Truncated {
			t.Errorf("InspectBlob(%q) = %#v, want complete classified binary", revision, result)
		}
	}
}

func TestBlobDetectsNULWithinReturnedRange(t *testing.T) {
	dir, commit := seedBlobRepository(t)
	t.Setenv("PATH", t.TempDir())

	content, binary, truncated, err := Blob(context.Background(), dir, commit, "binary", 3)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	if content != nil || !binary || truncated {
		t.Errorf("Blob = (%v, %v, %v), want nil, binary, complete", content, binary, truncated)
	}
}
