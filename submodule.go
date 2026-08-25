package clone

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/git-pkgs/purl"
)

// SubmoduleStatus describes whether a declared submodule is available at its
// pinned commit.
type SubmoduleStatus string

const (
	SubmoduleStatusInitialized SubmoduleStatus = "initialized"
	SubmoduleStatusUnavailable SubmoduleStatus = "unavailable"
)

// Submodule describes a gitlink declared by a checkout. Path is relative to
// the top-level checkout, URL is resolved, Commit is the exact gitlink object,
// and PURL is pinned to Commit. Initialized is true only when the submodule
// checkout's HEAD matches Commit. URL, PURL, and Error omit URL userinfo.
type Submodule struct {
	Path        string
	URL         string
	Commit      string
	PURL        string
	Initialized bool
	Status      SubmoduleStatus
	Error       string
}

type submoduleDefinition struct {
	configKey string
	path      string
	url       string
}

const gitmodulesBlob = "HEAD:.gitmodules"

// Submodules returns deterministic metadata for the submodules declared by
// the checkout in dir. Unavailable submodules are returned with Status set to
// SubmoduleStatusUnavailable rather than failing the query.
func Submodules(ctx context.Context, dir string) ([]Submodule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	modules, err := submodulesAt(ctx, dir, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(modules, func(i, j int) bool {
		return modules[i].Path < modules[j].Path
	})
	return modules, nil
}

func submodulesAt(ctx context.Context, dir, parentPath string) ([]Submodule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definitions, err := readSubmoduleDefinitions(ctx, dir)
	if err != nil {
		return nil, err
	}

	var modules []Submodule
	for _, definition := range definitions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		module := Submodule{
			Path:   path.Join(parentPath, definition.path),
			Status: SubmoduleStatusUnavailable,
		}
		commit, err := gitlinkCommit(ctx, dir, definition.path)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			module.Error = "gitlink commit is unavailable"
			modules = append(modules, module)
			continue
		}
		module.Commit = commit

		resolvedURL, err := submoduleURL(ctx, dir, definition)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			module.Error = "resolved repository URL is unavailable"
		} else {
			module.URL, module.PURL, err = submoduleIdentity(resolvedURL, commit)
			if err != nil {
				module.Error = "package URL is unavailable"
			}
		}

		childDir := pathFromGit(dir, definition.path)
		head, headErr := submoduleHead(ctx, childDir)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		switch {
		case headErr == nil && head == commit:
			module.Initialized = true
			module.Status = SubmoduleStatusInitialized
		case headErr == nil:
			addSubmoduleError(&module, fmt.Sprintf("checked out commit %s does not match gitlink %s", head, commit))
		default:
			addSubmoduleError(&module, "submodule checkout is unavailable")
		}
		modules = append(modules, module)

		if !module.Initialized {
			continue
		}
		nested, err := submodulesAt(ctx, childDir, module.Path)
		if err != nil {
			return nil, err
		}
		modules = append(modules, nested...)
	}
	return modules, nil
}

func readSubmoduleDefinitions(ctx context.Context, dir string) ([]submoduleDefinition, error) {
	if _, err := Run(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read checkout HEAD: %w", err)
	}
	if _, err := Run(ctx, dir, nil, "cat-file", "-e", gitmodulesBlob); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	out, err := Run(ctx, dir, nil,
		"config", "-z", "--blob", gitmodulesBlob, "--get-regexp", `^submodule\..*\.path$`,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if out == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("read .gitmodules: %w", err)
	}

	var definitions []submoduleDefinition
	for entry := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		key, submodulePath, ok := strings.Cut(entry, "\n")
		if !ok || !strings.HasSuffix(key, ".path") {
			return nil, fmt.Errorf("read .gitmodules: invalid path entry")
		}
		submodulePath, ok = SanitizePath(submodulePath)
		if !ok {
			return nil, fmt.Errorf("read .gitmodules: invalid submodule path")
		}
		configKey := strings.TrimSuffix(key, ".path")
		submoduleURL, err := gitConfigValue(ctx, dir, gitmodulesBlob, configKey+".url")
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			submoduleURL = ""
		}
		definitions = append(definitions, submoduleDefinition{
			configKey: configKey,
			path:      submodulePath,
			url:       submoduleURL,
		})
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].path < definitions[j].path
	})
	return definitions, nil
}

func gitConfigValue(ctx context.Context, dir, blob, key string) (string, error) {
	args := []string{"config", "-z"}
	if blob != "" {
		args = append(args, "--blob", blob)
	}
	args = append(args, "--get", key)
	out, err := Run(ctx, dir, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "\x00"), nil
}

func submoduleURL(ctx context.Context, dir string, definition submoduleDefinition) (string, error) {
	resolved, err := gitConfigValue(ctx, dir, "", definition.configKey+".url")
	if err == nil && resolved != "" {
		return resolved, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	parsed, parseErr := url.Parse(definition.url)
	if parseErr == nil && parsed.IsAbs() {
		return definition.url, nil
	}
	if err != nil {
		return "", err
	}
	if parseErr != nil {
		return "", parseErr
	}
	return "", fmt.Errorf("relative repository URL is unresolved")
}

func gitlinkCommit(ctx context.Context, dir, submodulePath string) (string, error) {
	out, err := Run(ctx, dir, nil, "ls-tree", "-z", "HEAD", "--", submodulePath)
	if err != nil {
		return "", err
	}
	entry := strings.TrimSuffix(out, "\x00")
	metadata, treePath, ok := strings.Cut(entry, "\t")
	if !ok || treePath != submodulePath {
		return "", fmt.Errorf("gitlink not found")
	}
	fields := strings.Fields(metadata)
	if len(fields) != 3 || fields[0] != "160000" || fields[1] != "commit" || !ValidCommit(fields[2]) {
		return "", fmt.Errorf("invalid gitlink")
	}
	return fields[2], nil
}

func submoduleHead(ctx context.Context, dir string) (string, error) {
	out, err := Run(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(out)
	if !ValidCommit(head) {
		return "", fmt.Errorf("invalid HEAD")
	}
	return head, nil
}

func submoduleIdentity(repositoryURL, commit string) (string, string, error) {
	redacted := RedactURL(repositoryURL)
	parsed, err := url.Parse(redacted)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid repository URL")
	}
	parsed.User = nil
	parsed.ForceQuery = false
	parsed.RawQuery = ""
	parsed.Fragment = ""
	credentialFreeURL := parsed.String()
	repositoryPath := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if strings.EqualFold(parsed.Hostname(), "github.com") {
		owner, name, ok := strings.Cut(repositoryPath, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return credentialFreeURL, "", fmt.Errorf("invalid GitHub repository URL")
		}
		return credentialFreeURL, purl.New("github", owner, name, commit, nil).String(), nil
	}

	name := path.Base(repositoryPath)
	if name == "." || name == "" {
		return credentialFreeURL, "", fmt.Errorf("repository name is unavailable")
	}
	vcsURL := "git+" + credentialFreeURL + "@" + commit
	identity := purl.New("generic", "", name, "", map[string]string{"vcs_url": vcsURL})
	return credentialFreeURL, identity.String(), nil
}

func pathFromGit(dir, submodulePath string) string {
	return filepath.Join(dir, filepath.FromSlash(submodulePath))
}

func addSubmoduleError(module *Submodule, message string) {
	if module.Error == "" {
		module.Error = message
		return
	}
	module.Error += "; " + message
}
