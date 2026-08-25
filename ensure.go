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
	return fmt.Sprintf("repository unreachable %s: %s", RedactURL(e.URL), e.Err)
}

func (e *UnreachableError) Unwrap() error {
	return e.Err
}

// EnsureOptions configures an EnsureWithOptions operation.
type EnsureOptions struct {
	Full              bool // Clone full history and unshallow an existing checkout.
	RecurseSubmodules bool // Initialize and update submodules recursively at depth 1.
}

// Ensure clones url into dst on its first call, then fetches and resets the
// checkout on later calls. A shallow clone is used unless full is true. An
// existing shallow clone is unshallowed when full changes to true. ref may
// be a branch, tag, commit ID, or empty for the remote's default branch.
func Ensure(ctx context.Context, retry Retry, url, dst, ref string, full bool) error {
	return EnsureWithOptions(ctx, retry, url, dst, ref, EnsureOptions{Full: full})
}

// EnsureWithOptions clones or updates a checkout like Ensure. When
// RecurseSubmodules is enabled, it also makes a best-effort attempt to
// initialize and update nested submodules with depth 1. A submodule failure
// does not fail the checkout, but context cancellation still does.
func EnsureWithOptions(ctx context.Context, retry Retry, url, dst, ref string, options EnsureOptions) error {
	err := ensure(ctx, retry, url, dst, ref, options)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &UnreachableError{URL: url, Err: err}
}

func ensure(ctx context.Context, retry Retry, url, dst, ref string, options EnsureOptions) error {
	if err := ValidateURL(url); err != nil {
		return err
	}
	if err := ValidateRef(ref); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		if err := fetchRef(ctx, retry, url, dst, ref, options.Full); err != nil {
			return err
		}
		return updateSubmodules(ctx, retry, dst, options.RecurseSubmodules)
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}

	// The credential helper is intentionally NOT disabled here (unlike
	// RemoteBranches/RemoteHead): a caller that reaches Ensure has already
	// decided to clone this URL, and disabling the helper would make private
	// repositories unreachable for callers that authenticate via stored git
	// credentials rather than embedding a token in the URL.
	args := []string{"clone", "--quiet"} //nolint:goconst // Git argv is clearer with literal subcommands and flags.
	if !options.Full {
		args = append(args, "--depth", "1")
	}
	args = append(args, "--", url, dst)
	out, err := doPinnedURL(ctx, retry, url, Command{
		Label: "clone",
		Env:   remoteEnv(),
		Args:  args,
		Reset: DestReset(dst),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	if ref != "" {
		if err := fetchRef(ctx, retry, url, dst, ref, options.Full); err != nil {
			return err
		}
	}
	return updateSubmodules(ctx, retry, dst, options.RecurseSubmodules)
}

func updateSubmodules(ctx context.Context, retry Retry, dst string, enabled bool) error {
	if !enabled {
		return nil
	}
	policy := retry.Resolved()
	if _, err := policy.Run(ctx, dst, remoteEnv(), "submodule", "sync", "--recursive"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if _, err := policy.Do(ctx, Command{
		Label: "submodule",
		Dir:   dst,
		Env:   remoteEnv(),
		Args:  []string{"submodule", "update", "--init", "--recursive", "--depth", "1"},
	}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	return nil
}

// fetchRef takes url only so it can be pinned against ambient insteadOf
// rewriting: the fetch addresses the remote by name, but Git resolves origin's
// stored URL -- the validated one clone recorded -- through the same rules.
func fetchRef(ctx context.Context, retry Retry, url, dst, ref string, full bool) error {
	policy := retry.Resolved()
	target := ref
	if target == "" {
		target = "HEAD" //nolint:goconst // Git's default ref is clearest by its literal name.
	}
	args := []string{"-C", dst, "fetch", "--quiet", "--no-recurse-submodules"} //nolint:goconst // Git argv is clearer with literal subcommands and flags.
	if full {
		out, _ := policy.Run(ctx, "", nil, "-C", dst, "rev-parse", "--is-shallow-repository")
		if strings.TrimSpace(out) == "true" {
			args = append(args, "--unshallow")
		}
	}
	args = append(args, "--", "origin", target)
	out, err := doPinnedURL(ctx, policy, url, Command{
		Label: "fetch", //nolint:goconst // Retry notices use the literal Git subcommand.
		Env:   remoteEnv(),
		Args:  args,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	out, err = policy.Run(ctx, "", nil,
		"-C", dst, "reset", "--quiet", "--hard", "--no-recurse-submodules", "FETCH_HEAD",
	)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// Head returns the object ID at HEAD in dir, or an empty string when dir is
// not a Git repository.
func Head(ctx context.Context, dir string) string {
	out, err := Run(ctx, dir, nil, "rev-parse", "HEAD") //nolint:goconst // Git argv is clearer with literal refs.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
