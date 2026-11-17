package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
	// SubmoduleCredentialURLs lists https URL prefixes, matched on path
	// segment boundaries, whose submodules are fetched with the ambient
	// credential helper. All other submodules are fetched without it.
	SubmoduleCredentialURLs []string
	// OnSubmoduleIssue, when set, is called for each submodule left out of
	// the checkout.
	OnSubmoduleIssue func(SubmoduleIssue)
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
	trusted, err := parseCredentialPrefixes(options.SubmoduleCredentialURLs)
	if err != nil {
		return err
	}
	update := submoduleUpdate{
		budget:  submoduleBudget{maxDepth: maxSubmoduleDepth, maxModules: maxSubmodules},
		trusted: trusted,
		onIssue: options.OnSubmoduleIssue,
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		if err := fetchRef(ctx, retry, url, dst, ref, options.Full); err != nil {
			return err
		}
		return updateSubmodules(ctx, retry, dst, options.RecurseSubmodules, update)
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}

	// The credential helper is intentionally NOT disabled here (unlike
	// RemoteBranches/RemoteHead): a caller that reaches Ensure has already
	// decided to clone this URL, and disabling the helper would make private
	// repositories unreachable for callers that authenticate via stored git
	// credentials rather than embedding a token in the URL. Submodule URLs
	// are not chosen by the caller, so updateSubmodules does disable it.
	args := []string{"clone", "--quiet"} //nolint:goconst // Git argv is clearer with literal subcommands and flags.
	if !options.Full {
		args = append(args, "--depth", "1")
	}
	args = append(args, "--", url, dst)
	out, err := doPinnedURL(ctx, retry, url, Command{
		Label: "clone",
		Env:   remoteEnv(),
		Args:  longPathArgs(args...),
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
	return updateSubmodules(ctx, retry, dst, options.RecurseSubmodules, update)
}

// Limits on recursive submodule updates, so nested or repeated gitlinks in a
// compact history cannot expand into an unbounded number of checkouts.
const (
	maxSubmoduleDepth = 8
	maxSubmodules     = 256
)

type submoduleBudget struct {
	maxDepth   int
	maxModules int
}

// updateSubmodules fetches dst's submodules when enabled.
func updateSubmodules(ctx context.Context, retry Retry, dst string, enabled bool, update submoduleUpdate) error {
	if !enabled {
		return nil
	}
	return updateSubmodulesWithin(ctx, retry, dst, update)
}

// updateSubmodulesWithin walks submodules breadth first, one repository's
// submodules per Git call. Each batch is counted before it is fetched, and the
// walk stops before exceeding the budget, keeping what was already fetched.
// Like git's own --recursive, it descends only into submodules checked out at
// their gitlink. Only context errors are returned.
func updateSubmodulesWithin(ctx context.Context, retry Retry, dst string, update submoduleUpdate) error {
	policy := retry.Resolved()
	queue := []pendingSubmodules{{dir: dst, depth: 1}}
	fetched := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth > update.budget.maxDepth {
			return update.reportPending(ctx, policy, append([]pendingSubmodules{current}, queue...), SubmoduleIssueDepthLimit)
		}
		links, err := submoduleGitlinks(ctx, policy, current.dir)
		if err != nil {
			return err
		}
		if len(links) == 0 {
			continue
		}
		if fetched+len(links) > update.budget.maxModules {
			update.reportLinks(ctx, policy, current, links, SubmoduleIssueModuleLimit, "")
			return update.reportPending(ctx, policy, queue, SubmoduleIssueModuleLimit)
		}
		fetched += len(links)
		outputs, err := updateSubmoduleLevel(ctx, policy, current.dir, links, update.trusted)
		if err != nil {
			return err
		}
		for _, link := range links {
			child := pathFromGit(current.dir, link.path)
			ok, err := checkedOutAt(ctx, policy, child, link.commit)
			if err != nil {
				return err
			}
			if ok {
				queue = append(queue, pendingSubmodules{
					dir:    child,
					prefix: joinSlash(current.prefix, link.path),
					depth:  current.depth + 1,
				})
				continue
			}
			update.reportLinks(ctx, policy, current, []gitlink{link}, SubmoduleIssueFetchFailed, outputs[link.path])
		}
	}
	return nil
}

// updateSubmoduleLevel syncs and updates exactly the given submodules of dir,
// returning the update output for each path. Submodule URLs come from the
// cloned .gitmodules, so the ambient credential helper is disabled except for
// URLs under a trusted prefix. Literal pathspecs keep a path from matching
// more submodules than were counted.
func updateSubmoduleLevel(ctx context.Context, policy Retry, dir string, links []gitlink, trusted []credentialPrefix) (map[string]string, error) {
	env := append(remoteEnv(), "GIT_LITERAL_PATHSPECS=1")
	paths := make([]string, len(links))
	for i, link := range links {
		paths[i] = link.path
	}
	noHelper := []string{"-c", "credential.helper="}
	syncArgs := slices.Concat(noHelper, []string{"submodule", "sync", "--"}, paths)
	if _, err := policy.Run(ctx, dir, env, longPathArgs(syncArgs...)...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}

	anonymous, authenticated := paths, []string(nil)
	if len(trusted) > 0 {
		// init resolves relative URLs into .git/config without fetching, so
		// the URL matched here is the one update will fetch.
		initArgs := append([]string{"submodule", "init", "--"}, paths...)
		if _, err := policy.Run(ctx, dir, env, longPathArgs(initArgs...)...); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		}
		anonymous = nil
		for _, link := range links {
			if credentialAllowed(trusted, submoduleURLConfig(ctx, policy, dir, link.name)) {
				authenticated = append(authenticated, link.path)
			} else {
				anonymous = append(anonymous, link.path)
			}
		}
	}

	outputs := make(map[string]string, len(paths))
	for _, batch := range []struct {
		paths  []string
		config []string
	}{{anonymous, noHelper}, {authenticated, nil}} {
		if len(batch.paths) == 0 {
			continue
		}
		args := slices.Concat(batch.config, []string{"submodule", "update", "--init", "--depth", "1", "--"}, batch.paths)
		out, err := policy.Do(ctx, Command{
			Label: "submodule",
			Dir:   dir,
			Env:   env,
			Args:  longPathArgs(args...),
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
		}
		for _, p := range batch.paths {
			outputs[p] = out
		}
	}
	return outputs, nil
}

type gitlink struct {
	name   string
	path   string
	commit string
}

// submoduleGitlinks returns the paths that dir's .gitmodules declares and HEAD
// records as gitlinks, sorted by path: the submodules one update of dir can
// fetch. A missing or unreadable .gitmodules yields none.
func submoduleGitlinks(ctx context.Context, policy Retry, dir string) ([]gitlink, error) {
	out, err := policy.Run(ctx, dir, nil,
		"config", "-z", "--blob", gitmodulesBlob, "--get-regexp", `^submodule\..*\.path$`,
	)
	if err != nil {
		return nil, ctx.Err()
	}
	declared := map[string]string{} // path to submodule name
	var candidates []string
	for entry := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		key, value, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		submodulePath, ok := SanitizePath(value)
		if _, seen := declared[submodulePath]; !ok || seen {
			continue
		}
		declared[submodulePath] = strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
		candidates = append(candidates, submodulePath)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	env := []string{"GIT_LITERAL_PATHSPECS=1"}
	out, err = policy.Run(ctx, dir, env, append([]string{"ls-tree", "-z", "HEAD", "--"}, candidates...)...)
	if err != nil {
		return nil, ctx.Err()
	}
	var links []gitlink
	for entry := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		metadata, treePath, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		name, ok := declared[treePath]
		if !ok {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" && ValidCommit(fields[2]) {
			links = append(links, gitlink{name: name, path: treePath, commit: fields[2]})
		}
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].path < links[j].path
	})
	return links, nil
}

// checkedOutAt reports whether dir is a real directory holding a submodule
// checkout whose HEAD is commit. Rejecting symlinks keeps the walk inside the
// tree; the ceiling stops an uninitialized directory from resolving to its
// parent's HEAD.
func checkedOutAt(ctx context.Context, policy Retry, dir, commit string) (bool, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return false, nil
	}
	ceiling, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return false, nil
	}
	out, err := policy.Run(ctx, dir, []string{"GIT_CEILING_DIRECTORIES=" + ceiling}, "rev-parse", "HEAD")
	if err != nil {
		return false, ctx.Err()
	}
	return strings.TrimSpace(out) == commit, nil
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
		Args:  longPathArgs(args...),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	out, err = policy.Run(ctx, "", nil,
		longPathArgs("-C", dst, "reset", "--quiet", "--hard", "--no-recurse-submodules", "FETCH_HEAD")...,
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
