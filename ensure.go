package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dirPerm = 0o755

// UnreachableError reports a clone or fetch failure for URL.
type UnreachableError struct {
	URL string
	Err error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("repository unreachable %s: %s", e.URL, e.Err)
}

func (e *UnreachableError) Unwrap() error {
	return e.Err
}

// Ensure clones url into dst on its first call, then fetches and resets the
// checkout on later calls. A shallow clone is used unless full is true. An
// existing shallow clone is unshallowed when full changes to true. ref may
// be a branch, tag, commit ID, or empty for the remote's default branch.
func Ensure(ctx context.Context, retry Retry, url, dst, ref string, full bool) error {
	err := ensure(ctx, retry, url, dst, ref, full)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &UnreachableError{URL: url, Err: err}
}

func ensure(ctx context.Context, retry Retry, url, dst, ref string, full bool) error {
	if err := ValidateURL(url); err != nil {
		return err
	}
	if err := ValidateRef(ref); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		return fetchRef(ctx, retry, dst, ref, full)
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}

	args := []string{gitClone, quietFlag}
	if !full {
		args = append(args, "--depth", "1")
	}
	args = append(args, "--", url, dst)
	out, err := retry.Do(ctx, Command{
		Label: gitClone,
		Env:   []string{"GIT_PROTOCOL_FROM_USER=0"},
		Args:  args,
		Reset: DestReset(dst),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	if ref != "" {
		return fetchRef(ctx, retry, dst, ref, full)
	}
	return nil
}

func fetchRef(ctx context.Context, retry Retry, dst, ref string, full bool) error {
	policy := retry.Resolved()
	target := ref
	if target == "" {
		target = gitHEAD
	}
	args := []string{"-C", dst, gitFetch, quietFlag}
	if full {
		out, _ := policy.Run(ctx, "", nil, "-C", dst, "rev-parse", "--is-shallow-repository")
		if strings.TrimSpace(out) == "true" {
			args = append(args, "--unshallow")
		}
	}
	args = append(args, "--", "origin", target)
	out, err := policy.Do(ctx, Command{Label: gitFetch, Args: args})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	out, err = policy.Run(ctx, "", nil, "-C", dst, "reset", "--quiet", "--hard", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// Head returns the object ID at HEAD in dir, or an empty string when dir is
// not a Git repository.
func Head(ctx context.Context, dir string) string {
	out, err := Run(ctx, dir, nil, "rev-parse", gitHEAD)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
