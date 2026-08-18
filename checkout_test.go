package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type checkoutFixture struct {
	dir      string
	firstSHA string
	headSHA  string
}

func newCheckoutFixture(t *testing.T) checkoutFixture {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet", "-b", "main")
	marker := filepath.Join(dir, "version.txt")
	if err := os.WriteFile(marker, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "version.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "first")
	firstSHA := runGitTest(t, dir, "rev-parse", "HEAD")
	runGitTest(t, dir, "tag", "v1", firstSHA)
	runGitTest(t, dir, "tag", "-a", "v1-annotated", "-m", "v1-annotated", firstSHA)
	runGitTest(t, dir, "tag", "v1.0.0+build", firstSHA)

	if err := os.WriteFile(marker, []byte("head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "commit", "--quiet", "-am", "head")
	headSHA := runGitTest(t, dir, "rev-parse", "HEAD")
	runGitTest(t, dir, "branch", "v1", headSHA)

	return checkoutFixture{dir: dir, firstSHA: firstSHA, headSHA: headSHA}
}

func TestCheckoutTagChecksOutExactTagAndRestoresBranch(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := Head(context.Background(), fixture.dir); got != fixture.firstSHA {
		t.Fatalf("tag HEAD = %q, want %q", got, fixture.firstSHA)
	}
	if _, err := Run(context.Background(), fixture.dir, nil, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Fatal("tag checkout left HEAD attached")
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, "version.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := Head(context.Background(), fixture.dir); got != fixture.headSHA {
		t.Errorf("restored HEAD = %q, want %q", got, fixture.headSHA)
	}
	branch, err := Run(context.Background(), fixture.dir, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(branch); got != "refs/heads/main" {
		t.Errorf("restored branch = %q, want refs/heads/main", got)
	}
	content, err := os.ReadFile(filepath.Join(fixture.dir, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "head\n" {
		t.Errorf("restored content = %q, want %q", got, "head\n")
	}
}

func TestCheckoutTagPeelsAnnotatedTag(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1-annotated")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restore(context.Background()); err != nil {
			t.Errorf("restore: %v", err)
		}
	}()
	if got := Head(context.Background(), fixture.dir); got != fixture.firstSHA {
		t.Errorf("tag HEAD = %q, want %q", got, fixture.firstSHA)
	}
}

func TestCheckoutTagAcceptsGitValidTagOutsideValidateRefPolicy(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1.0.0+build")
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutTagRestoresDetachedHead(t *testing.T) {
	fixture := newCheckoutFixture(t)
	runGitTest(t, fixture.dir, "checkout", "--quiet", "--detach", fixture.headSHA)
	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := Head(context.Background(), fixture.dir); got != fixture.headSHA {
		t.Errorf("restored HEAD = %q, want %q", got, fixture.headSHA)
	}
	if _, err := Run(context.Background(), fixture.dir, nil, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Fatal("restore attached a previously detached HEAD")
	}
}

func TestCheckoutTagRestoreRefusesMovedBranch(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.dir, "update-ref", "refs/heads/main", fixture.firstSHA)
	if err := restore(context.Background()); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("restore error = %v, want moved-branch error", err)
	}
	if got := Head(context.Background(), fixture.dir); got != fixture.headSHA {
		t.Errorf("HEAD after refused branch attachment = %q, want %q", got, fixture.headSHA)
	}
	if _, err := Run(context.Background(), fixture.dir, nil, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Fatal("restore attached HEAD to a moved branch")
	}
}

func TestCheckoutTagRejectsMissingAndRevisionLikeTagsWithoutMovingHead(t *testing.T) {
	fixture := newCheckoutFixture(t)
	for _, test := range []struct {
		tag      string
		notFound bool
	}{
		{tag: "missing", notFound: true},
		{tag: "--ignore-skip-worktree-bits", notFound: true},
		{tag: "v1~1"},
	} {
		t.Run(test.tag, func(t *testing.T) {
			restore, err := CheckoutTag(context.Background(), fixture.dir, test.tag)
			if err == nil || restore != nil {
				t.Fatalf("CheckoutTag(%q) restore set = %t, error = %v; want nil restore and error", test.tag, restore != nil, err)
			}
			if errors.Is(err, ErrTagNotFound) != test.notFound {
				t.Errorf("error = %v, ErrTagNotFound = %t", err, test.notFound)
			}
			if got := Head(context.Background(), fixture.dir); got != fixture.headSHA {
				t.Errorf("HEAD after rejected tag = %q, want %q", got, fixture.headSHA)
			}
		})
	}
}

func TestCheckoutTagHonorsContexts(t *testing.T) {
	fixture := newCheckoutFixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckoutTag(cancelled, fixture.dir, "v1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkout error = %v, want context.Canceled", err)
	}

	restore, err := CheckoutTag(context.Background(), fixture.dir, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("restore error = %v, want context.Canceled", err)
	}
	if err := restore(context.Background()); err != nil {
		t.Fatalf("cleanup restore: %v", err)
	}
}

func TestCheckoutTagRejectsNonCommitTag(t *testing.T) {
	fixture := newCheckoutFixture(t)
	blob := runGitTest(t, fixture.dir, "hash-object", "-w", "version.txt")
	runGitTest(t, fixture.dir, "tag", "blob", blob)
	if _, err := CheckoutTag(context.Background(), fixture.dir, "blob"); err == nil || errors.Is(err, ErrTagNotFound) {
		t.Fatalf("error = %v, want non-commit tag error", err)
	}
}
