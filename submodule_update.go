package clone

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// SubmoduleIssueReason says why a declared submodule is missing after
// EnsureWithOptions.
type SubmoduleIssueReason string

const (
	SubmoduleIssueFetchFailed SubmoduleIssueReason = "fetch_failed"
	SubmoduleIssueDepthLimit  SubmoduleIssueReason = "depth_limit"
	SubmoduleIssueModuleLimit SubmoduleIssueReason = "module_limit"
)

// SubmoduleIssue describes a submodule the best-effort submodule step left
// out. Path is relative to the top-level checkout. URL omits userinfo and is
// empty when it could not be read. Output is Git's output for a failed fetch,
// with URL userinfo redacted. Submodules nested beneath a skipped submodule
// are not reported.
type SubmoduleIssue struct {
	Path   string
	URL    string
	Reason SubmoduleIssueReason
	Output string
}

type credentialPrefix struct {
	host string
	path string
}

// ValidateSubmoduleCredentialURLs reports the first entry EnsureOptions would
// reject in SubmoduleCredentialURLs, so callers can check configuration
// before cloning.
func ValidateSubmoduleCredentialURLs(urls []string) error {
	_, err := parseCredentialPrefixes(urls)
	return err
}

func parseCredentialPrefixes(raw []string) ([]credentialPrefix, error) {
	prefixes := make([]credentialPrefix, 0, len(raw))
	for _, entry := range raw {
		u, ok := strictHTTPSURL(entry)
		if !ok {
			return nil, fmt.Errorf("invalid submodule credential URL %q: must be https with no userinfo, query, fragment, or dot segments", RedactURL(entry))
		}
		prefixes = append(prefixes, credentialPrefix{
			host: strings.ToLower(u.Host),
			path: strings.TrimSuffix(u.Path, "/"),
		})
	}
	return prefixes, nil
}

// strictHTTPSURL parses raw and rejects anything Git might resolve to a
// different repository than the literal path suggests.
func strictHTTPSURL(raw string) (*url.URL, bool) {
	if ValidateURL(raw) != nil || strings.ContainsAny(raw, "\\ ") {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.RawPath != "" ||
		u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return nil, false
	}
	segments := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	for i, segment := range segments {
		if segment == "." || segment == ".." || (segment == "" && i > 0) {
			return nil, false
		}
	}
	return u, true
}

func credentialAllowed(prefixes []credentialPrefix, raw string) bool {
	u, ok := strictHTTPSURL(raw)
	if !ok {
		return false
	}
	host := strings.ToLower(u.Host)
	return slices.ContainsFunc(prefixes, func(p credentialPrefix) bool {
		return host == p.host && (u.Path == p.path || strings.HasPrefix(u.Path, p.path+"/"))
	})
}

type submoduleUpdate struct {
	budget  submoduleBudget
	trusted []credentialPrefix
	onIssue func(SubmoduleIssue)
}

type pendingSubmodules struct {
	dir    string
	prefix string // slash path of dir relative to the top-level checkout
	depth  int    // depth of the submodules declared in dir
}

func (u submoduleUpdate) reportLinks(ctx context.Context, policy Retry, p pendingSubmodules, links []gitlink, reason SubmoduleIssueReason, output string) {
	if u.onIssue == nil {
		return
	}
	for _, link := range links {
		u.onIssue(SubmoduleIssue{
			Path:   joinSlash(p.prefix, link.path),
			URL:    submoduleIssueURL(ctx, policy, p.dir, link.name),
			Reason: reason,
			Output: redactOutput(output),
		})
	}
}

// reportPending reports the submodules declared by each pending directory as
// skipped for reason.
func (u submoduleUpdate) reportPending(ctx context.Context, policy Retry, queue []pendingSubmodules, reason SubmoduleIssueReason) error {
	if u.onIssue == nil {
		return nil
	}
	for _, p := range queue {
		links, err := submoduleGitlinks(ctx, policy, p.dir)
		if err != nil {
			return err
		}
		u.reportLinks(ctx, policy, p, links, reason, "")
	}
	return nil
}

func submoduleURLConfig(ctx context.Context, policy Retry, dir, name string) string {
	out, err := policy.Run(ctx, dir, nil, "config", "-z", "--get", "submodule."+name+".url")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(out, "\x00")
}

func submoduleIssueURL(ctx context.Context, policy Retry, dir, name string) string {
	raw := submoduleURLConfig(ctx, policy, dir, name)
	if raw == "" {
		out, err := policy.Run(ctx, dir, nil, "config", "-z", "--blob", gitmodulesBlob, "--get", "submodule."+name+".url")
		if err != nil {
			return ""
		}
		raw = strings.TrimSuffix(out, "\x00")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

var outputUserinfo = regexp.MustCompile(`://[^/\s@]+@`)

func redactOutput(out string) string {
	return outputUserinfo.ReplaceAllString(strings.TrimSpace(out), "://REDACTED@")
}

func joinSlash(prefix, p string) string {
	if prefix == "" {
		return p
	}
	return prefix + "/" + p
}
