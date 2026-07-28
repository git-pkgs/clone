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
		Label: gitLSRemote,
		Env:   []string{"GIT_TERMINAL_PROMPT=0"},
		Args:  []string{"-c", "credential.helper=", gitLSRemote, "--heads", "--", url},
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
		Label: gitLSRemote,
		Env:   []string{"GIT_TERMINAL_PROMPT=0"},
		Args:  []string{gitLSRemote, "--", url, gitHEAD},
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSpace(ref) == gitHEAD {
			return strings.TrimSpace(sha), nil
		}
	}
	return "", fmt.Errorf("no HEAD in ls-remote output for %q", url)
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
