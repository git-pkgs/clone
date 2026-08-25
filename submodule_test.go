package clone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testRepository struct {
	dir    string
	commit string
}

func newTestRepository(t *testing.T, filename string) testRepository {
	t.Helper()

	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(filename+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", filename)
	runGitTest(t, dir, "commit", "--quiet", "-m", "initial")
	return testRepository{dir: dir, commit: runGitTest(t, dir, "rev-parse", "HEAD")}
}

func configureTestURLs(t *testing.T, repositories map[string]string) {
	t.Helper()

	i := 0
	for repositoryURL, dir := range repositories {
		t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), "url.file://"+dir+".insteadOf")
		t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), repositoryURL)
		i++
	}
	t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), "protocol.file.allow")
	t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), "always")
	t.Setenv("GIT_CONFIG_COUNT", fmt.Sprint(i+1))
	t.Setenv("GIT_ALLOW_PROTOCOL", "https:file")
}

func TestSubmodulesReportsInitializedNestedIdentities(t *testing.T) {
	requireGit(t)

	leaf := newTestRepository(t, "leaf.txt")
	outer := newTestRepository(t, "outer.txt")
	generic := newTestRepository(t, "generic.txt")
	parent := newTestRepository(t, "parent.txt")

	const (
		parentURL  = "https://clone.test/parent.git"
		genericURL = "https://clone.test/generic.git"
		outerURL   = "https://github.com/Example/Outer.git"
		leafURL    = "https://github.com/Example/Leaf.git"
	)
	configureTestURLs(t, map[string]string{
		parentURL:  parent.dir,
		genericURL: generic.dir,
		outerURL:   outer.dir,
		leafURL:    leaf.dir,
	})

	runGitTest(t, outer.dir, "submodule", "add", "--quiet", "--name", "leaf", leafURL, "deps/leaf")
	runGitTest(t, outer.dir, "commit", "--quiet", "-am", "add nested submodule")
	outer.commit = runGitTest(t, outer.dir, "rev-parse", "HEAD")

	runGitTest(t, parent.dir, "submodule", "add", "--quiet", "--name", "generic", genericURL, "third_party/generic")
	runGitTest(t, parent.dir, "config", "--file", ".gitmodules", "submodule.generic.url", "../generic.git")
	runGitTest(t, parent.dir, "submodule", "add", "--quiet", "--name", "outer", outerURL, "vendor/outer")
	runGitTest(t, parent.dir, "commit", "--quiet", "-am", "add submodules")
	parent.commit = runGitTest(t, parent.dir, "rev-parse", "HEAD")

	cache := Cache{Root: t.TempDir(), RecurseSubmodules: true}
	dst := filepath.Join(t.TempDir(), "workspace", "src")
	commit, err := cache.Prepare(context.Background(), parentURL, "", dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if commit != parent.commit {
		t.Fatalf("commit = %q, want %q", commit, parent.commit)
	}
	if err := os.WriteFile(filepath.Join(dst, ".gitmodules"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	modules, err := Submodules(context.Background(), dst)
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	want := []Submodule{
		{
			Path:        "third_party/generic",
			URL:         genericURL,
			Commit:      generic.commit,
			PURL:        fmt.Sprintf("pkg:generic/generic?vcs_url=git%%2Bhttps:%%2F%%2Fclone.test%%2Fgeneric.git%%40%s", generic.commit),
			Initialized: true,
			Status:      SubmoduleStatusInitialized,
		},
		{
			Path:        "vendor/outer",
			URL:         outerURL,
			Commit:      outer.commit,
			PURL:        "pkg:github/example/outer@" + outer.commit,
			Initialized: true,
			Status:      SubmoduleStatusInitialized,
		},
		{
			Path:        "vendor/outer/deps/leaf",
			URL:         leafURL,
			Commit:      leaf.commit,
			PURL:        "pkg:github/example/leaf@" + leaf.commit,
			Initialized: true,
			Status:      SubmoduleStatusInitialized,
		},
	}
	if !reflect.DeepEqual(modules, want) {
		t.Fatalf("Submodules() = %#v, want %#v", modules, want)
	}
}

func TestCachePrepareSyncsChangedSubmoduleURL(t *testing.T) {
	requireGit(t)

	first := newTestRepository(t, "first.txt")
	second := newTestRepository(t, "second.txt")
	parent := newTestRepository(t, "parent.txt")
	const (
		parentURL = "https://clone.test/retargeted.git"
		firstURL  = "https://github.com/example/first.git"
		secondURL = "https://github.com/example/second.git"
	)
	configureTestURLs(t, map[string]string{
		parentURL: parent.dir,
		firstURL:  first.dir,
		secondURL: second.dir,
	})

	runGitTest(t, parent.dir, "submodule", "add", "--quiet", "--name", "dependency", firstURL, "deps/library")
	runGitTest(t, parent.dir, "commit", "--quiet", "-am", "add submodule")

	cache := Cache{Root: t.TempDir(), RecurseSubmodules: true}
	dst := filepath.Join(t.TempDir(), "workspace", "src")
	if _, err := cache.Prepare(context.Background(), parentURL, "", dst); err != nil {
		t.Fatalf("first Prepare: %v", err)
	}

	runGitTest(t, parent.dir, "config", "--file", ".gitmodules", "submodule.dependency.url", secondURL)
	runGitTest(t, parent.dir, "add", ".gitmodules")
	runGitTest(t, parent.dir, "update-index", "--cacheinfo", "160000,"+second.commit+",deps/library")
	runGitTest(t, parent.dir, "commit", "--quiet", "-m", "retarget submodule")

	if _, err := cache.Prepare(context.Background(), parentURL, "", dst); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dst, "deps", "library", "second.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second.txt\n" {
		t.Errorf("submodule content = %q", content)
	}

	modules, err := Submodules(context.Background(), dst)
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	want := []Submodule{{
		Path:        "deps/library",
		URL:         secondURL,
		Commit:      second.commit,
		PURL:        "pkg:github/example/second@" + second.commit,
		Initialized: true,
		Status:      SubmoduleStatusInitialized,
	}}
	if !reflect.DeepEqual(modules, want) {
		t.Fatalf("Submodules() = %#v, want %#v", modules, want)
	}
}

func TestSubmodulesReportsUnavailableWithoutCredentials(t *testing.T) {
	requireGit(t)

	missing := newTestRepository(t, "missing.txt")
	parent := newTestRepository(t, "parent.txt")
	const (
		parentURL = "https://clone.test/unavailable.git"
		username  = "credential-user"
		secret    = "submodule-secret"
	)
	credentialURL := "https://" + username + ":" + secret + "@127.0.0.1:1/owner/missing.git"
	configureTestURLs(t, map[string]string{parentURL: parent.dir})

	runGitTest(t, parent.dir, "config", "--file", ".gitmodules", "submodule.missing.path", "deps/missing")
	runGitTest(t, parent.dir, "config", "--file", ".gitmodules", "submodule.missing.url", credentialURL)
	runGitTest(t, parent.dir, "add", ".gitmodules")
	runGitTest(t, parent.dir, "update-index", "--add", "--cacheinfo", "160000,"+missing.commit+",deps/missing")
	runGitTest(t, parent.dir, "commit", "--quiet", "-m", "add unavailable submodule")
	parent.commit = runGitTest(t, parent.dir, "rev-parse", "HEAD")

	cache := Cache{
		Root:              t.TempDir(),
		RecurseSubmodules: true,
		Retry:             Retry{Attempts: 1},
	}
	dst := filepath.Join(t.TempDir(), "workspace", "src")
	commit, err := cache.Prepare(context.Background(), parentURL, "", dst)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if commit != parent.commit {
		t.Fatalf("commit = %q, want %q", commit, parent.commit)
	}

	modules, err := Submodules(context.Background(), dst)
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("len(Submodules()) = %d, want 1", len(modules))
	}
	module := modules[0]
	if module.Path != "deps/missing" || module.Commit != missing.commit {
		t.Errorf("submodule path and commit = %q, %q", module.Path, module.Commit)
	}
	if module.URL != "https://127.0.0.1:1/owner/missing.git" {
		t.Error("URL is not credential-free")
	}
	wantPURL := fmt.Sprintf(
		"pkg:generic/missing?vcs_url=git%%2Bhttps:%%2F%%2F127.0.0.1:1%%2Fowner%%2Fmissing.git%%40%s",
		missing.commit,
	)
	if module.PURL != wantPURL {
		t.Error("PURL is not the expected credential-free identity")
	}
	if module.Initialized || module.Status != SubmoduleStatusUnavailable || module.Error == "" {
		t.Error("unavailable submodule status is incomplete")
	}
	metadata := fmt.Sprintf("%+v", modules)
	if strings.Contains(metadata, username) || strings.Contains(metadata, secret) {
		t.Fatal("submodule metadata contains credential")
	}
}

func TestSubmodulesReturnsEmptyForCheckoutWithoutSubmodules(t *testing.T) {
	requireGit(t)

	repository := newTestRepository(t, "file.txt")
	modules, err := Submodules(context.Background(), repository.dir)
	if err != nil {
		t.Fatalf("Submodules: %v", err)
	}
	if len(modules) != 0 {
		t.Fatalf("Submodules() = %#v, want none", modules)
	}
}

func TestSubmoduleURLReturnsStableErrorForUninitializedRelativeURL(t *testing.T) {
	requireGit(t)

	repository := newTestRepository(t, "file.txt")
	definition := submoduleDefinition{
		configKey: "submodule.missing",
		url:       "../missing.git",
	}
	_, err := submoduleURL(context.Background(), repository.dir, definition)
	if err == nil || err.Error() != "relative repository URL is unresolved" {
		t.Fatalf("error = %v, want stable unresolved-relative-URL error", err)
	}
}
