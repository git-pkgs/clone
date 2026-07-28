package clone

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// RemoteBranches returns the sorted branch names advertised by url. It
// disables terminal prompts and the ambient credential helper.
func RemoteBranches(ctx context.Context, retry Retry, url string) ([]string, error) {
	if err := ValidateURL(url); err != nil {
		return nil, err
	}
	out, err := retry.Do(ctx, Command{
		Args:  []string{"-c", "credential.helper=", "ls-remote", "--heads", "--", url}, //nolint:goconst // Git argv is clearer with literal subcommands.
		Label: "ls-remote",                                                             //nolint:goconst // Retry notices use the literal Git subcommand.
		Env:   remoteEnv(),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return parseRemoteHeads(out), nil
}

// RemoteHead returns the object ID advertised as HEAD by url.
func RemoteHead(ctx context.Context, retry Retry, url string) (string, error) {
	if err := ValidateURL(url); err != nil {
		return "", err
	}
	out, err := retry.Do(ctx, Command{
		Args:  []string{"-c", "credential.helper=", "ls-remote", "--", url, "HEAD"}, //nolint:goconst // Git argv is clearer with literal subcommands and refs.
		Label: "ls-remote",                                                          //nolint:goconst // Retry notices use the literal Git subcommand.
		Env:   remoteEnv(),
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSpace(ref) == "HEAD" {
			return strings.TrimSpace(sha), nil
		}
	}
	return "", fmt.Errorf("no HEAD in ls-remote output for %q", RedactURL(url))
}

func parseRemoteHeads(out string) []string {
	seen := map[string]bool{}
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		_, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
