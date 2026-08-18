package clone

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrTagNotFound reports that an exact local tag does not exist.
var ErrTagNotFound = errors.New("tag not found")

// CheckoutTag force-checks out the commit named by an exact local tag in
// detached-HEAD state. It returns a function that force-restores the previous
// HEAD commit and reattaches its branch when that branch has not moved.
//
// CheckoutTag does not fetch. It discards tracked working-tree and index
// changes both when checking out the tag and when restoring the previous HEAD.
// The caller supplies the restoration context so cleanup can continue with a
// fresh context after the original operation is cancelled.
func CheckoutTag(ctx context.Context, dir, tag string) (restore func(context.Context) error, err error) {
	tagRef := "refs/tags/" + tag
	if _, err := Run(ctx, dir, nil, "check-ref-format", tagRef); err != nil {
		return nil, fmt.Errorf("invalid tag %q: %w", tag, err)
	}

	previous, err := headCommit(ctx, dir)
	if err != nil {
		return nil, err
	}
	branch, err := headBranch(ctx, dir)
	if err != nil {
		return nil, err
	}

	commit, err := tagCommit(ctx, dir, tag, tagRef)
	if err != nil {
		return nil, err
	}
	if err := checkoutCommit(ctx, dir, commit); err != nil {
		return nil, fmt.Errorf("checkout tag %q: %w", tag, err)
	}

	return func(restoreCtx context.Context) error {
		if err := checkoutCommit(restoreCtx, dir, previous); err != nil {
			return fmt.Errorf("restore HEAD %s: %w", previous, err)
		}
		if branch == "" {
			return nil
		}
		current, err := exactRefCommit(restoreCtx, dir, branch)
		if err != nil {
			return fmt.Errorf("restore branch %q: %w", branch, err)
		}
		if current != previous {
			return fmt.Errorf("restore branch %q: moved from %s to %s", branch, previous, current)
		}
		if _, err := Run(restoreCtx, dir, nil, "symbolic-ref", "HEAD", branch); err != nil {
			return fmt.Errorf("restore branch %q: %w", branch, err)
		}
		return nil
	}, nil
}

func headCommit(ctx context.Context, dir string) (string, error) {
	out, err := Run(ctx, dir, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	commit := strings.TrimSpace(out)
	if !ValidCommit(commit) {
		return "", fmt.Errorf("resolve HEAD: invalid commit %q", commit)
	}
	return commit, nil
}

func headBranch(ctx context.Context, dir string) (string, error) {
	out, err := Run(ctx, dir, nil, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	if gitExitCode(err) == 1 {
		return "", nil
	}
	return "", fmt.Errorf("resolve HEAD branch: %w", err)
}

func tagCommit(ctx context.Context, dir, tag, tagRef string) (string, error) {
	if _, err := Run(ctx, dir, nil, "show-ref", "--verify", "--quiet", tagRef); err != nil {
		if gitExitCode(err) == 1 {
			return "", fmt.Errorf("%w: %q", ErrTagNotFound, tag)
		}
		return "", fmt.Errorf("resolve tag %q: %w", tag, err)
	}
	object, err := Run(ctx, dir, nil, "show-ref", "--verify", "--hash", tagRef)
	if err != nil {
		return "", fmt.Errorf("resolve tag %q: %w", tag, err)
	}
	object = strings.TrimSpace(object)
	if !ValidCommit(object) {
		return "", fmt.Errorf("resolve tag %q: invalid object %q", tag, object)
	}

	out, err := Run(ctx, dir, nil, "rev-parse", "--verify", object+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve tag %q commit: %w", tag, err)
	}
	commit := strings.TrimSpace(out)
	if !ValidCommit(commit) {
		return "", fmt.Errorf("resolve tag %q commit: invalid commit %q", tag, commit)
	}
	return commit, nil
}

func exactRefCommit(ctx context.Context, dir, ref string) (string, error) {
	out, err := Run(ctx, dir, nil, "show-ref", "--verify", "--hash", ref)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(out)
	if !ValidCommit(commit) {
		return "", fmt.Errorf("invalid commit %q", commit)
	}
	return commit, nil
}

func checkoutCommit(ctx context.Context, dir, commit string) error {
	if !ValidCommit(commit) {
		return fmt.Errorf("invalid commit %q", commit)
	}
	_, err := Run(ctx, dir, nil,
		"-c", "advice.detachedHead=false", "checkout", "--force", "--detach", commit)
	return err
}

func gitExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
