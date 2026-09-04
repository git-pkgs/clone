package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type originFixture struct {
	dir        string
	url        string
	firstSHA   string
	mainSHA    string
	featureSHA string
}

type submoduleOriginFixture struct {
	origin       originFixture
	submoduleDir string
}

func newOriginFixture(t *testing.T) originFixture {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	runGitTest(t, dir, "init", "--quiet", "-b", "main")
	runGitTest(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "file.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", "first")
	firstSHA := runGitTest(t, dir, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "commit", "--quiet", "-am", "main")
	mainSHA := runGitTest(t, dir, "rev-parse", "HEAD")
	runGitTest(t, dir, "tag", "v1", firstSHA)

	runGitTest(t, dir, "checkout", "--quiet", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "commit", "--quiet", "-am", "feature")
	featureSHA := runGitTest(t, dir, "rev-parse", "HEAD")
	runGitTest(t, dir, "checkout", "--quiet", "main")

	url := "https://clone.test/repository"
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+dir+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", url)
	t.Setenv("GIT_CONFIG_KEY_1", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_1", "always")
	// remoteEnv defaults to https-only; the fixture routes through file://.
	t.Setenv("GIT_ALLOW_PROTOCOL", "https:file")

	return originFixture{
		dir:        dir,
		url:        url,
		firstSHA:   firstSHA,
		mainSHA:    mainSHA,
		featureSHA: featureSHA,
	}
}

func newSubmoduleOriginFixture(t *testing.T) submoduleOriginFixture {
	t.Helper()
	requireGit(t)

	submoduleDir := t.TempDir()
	runGitTest(t, submoduleDir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(submoduleDir, "vendor.c"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, submoduleDir, "add", "vendor.c")
	runGitTest(t, submoduleDir, "commit", "--quiet", "-m", "first")

	origin := newOriginFixture(t)
	runGitTest(t, origin.dir, "submodule", "add", "--quiet", "file://"+submoduleDir, "vendor/library")
	runGitTest(t, origin.dir, "commit", "--quiet", "-m", "add submodule")
	origin.mainSHA = runGitTest(t, origin.dir, "rev-parse", "HEAD")

	return submoduleOriginFixture{origin: origin, submoduleDir: submoduleDir}
}

func (f submoduleOriginFixture) update(t *testing.T, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(f.submoduleDir, "vendor.c"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.submoduleDir, "commit", "--quiet", "-am", "update submodule")
	submoduleSHA := runGitTest(t, f.submoduleDir, "rev-parse", "HEAD")

	checkout := filepath.Join(f.origin.dir, "vendor", "library")
	runGitTest(t, checkout, "fetch", "--quiet", "origin", "main")
	runGitTest(t, checkout, "checkout", "--quiet", submoduleSHA)
	runGitTest(t, f.origin.dir, "add", "vendor/library")
	runGitTest(t, f.origin.dir, "commit", "--quiet", "-m", "update submodule pointer")
}

func TestEnsureClonesFetchesRefsAndUnshallows(t *testing.T) {
	origin := newOriginFixture(t)
	dst := filepath.Join(t.TempDir(), "nested", "checkout")
	ctx := context.Background()

	if err := Ensure(ctx, Retry{}, origin.url, dst, "", false); err != nil {
		t.Fatalf("initial Ensure: %v", err)
	}
	if got := Head(ctx, dst); got != origin.mainSHA {
		t.Fatalf("initial HEAD = %q, want %q", got, origin.mainSHA)
	}
	if got := runGitTest(t, dst, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("initial checkout shallow = %q, want true", got)
	}

	if err := os.WriteFile(filepath.Join(origin.dir, "file.txt"), []byte("new main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, origin.dir, "commit", "--quiet", "-am", "new main")
	newMainSHA := runGitTest(t, origin.dir, "rev-parse", "HEAD")
	if err := Ensure(ctx, Retry{}, origin.url, dst, "", false); err != nil {
		t.Fatalf("update default branch: %v", err)
	}
	if got := Head(ctx, dst); got != newMainSHA {
		t.Errorf("updated HEAD = %q, want %q", got, newMainSHA)
	}

	cases := []struct {
		ref  string
		want string
	}{
		{"feature", origin.featureSHA},
		{"v1", origin.firstSHA},
		{origin.firstSHA, origin.firstSHA},
	}
	for _, test := range cases {
		if err := Ensure(ctx, Retry{}, origin.url, dst, test.ref, false); err != nil {
			t.Fatalf("Ensure ref %q: %v", test.ref, err)
		}
		if got := Head(ctx, dst); got != test.want {
			t.Errorf("HEAD after ref %q = %q, want %q", test.ref, got, test.want)
		}
	}

	if err := Ensure(ctx, Retry{}, origin.url, dst, "", true); err != nil {
		t.Fatalf("unshallow: %v", err)
	}
	if got := Head(ctx, dst); got != newMainSHA {
		t.Errorf("HEAD after unshallow = %q, want %q", got, newMainSHA)
	}
	if got := runGitTest(t, dst, "rev-parse", "--is-shallow-repository"); got != "false" {
		t.Errorf("checkout shallow after full Ensure = %q, want false", got)
	}
}

func TestEnsureWithOptionsInitializesAndUpdatesShallowSubmodules(t *testing.T) {
	fixture := newSubmoduleOriginFixture(t)
	dst := filepath.Join(t.TempDir(), "checkout")
	ctx := context.Background()
	contentPath := filepath.Join(dst, "vendor", "library", "vendor.c")

	if err := Ensure(ctx, Retry{}, fixture.origin.url, dst, "", false); err != nil {
		t.Fatalf("Ensure without submodules: %v", err)
	}
	if _, err := os.Stat(contentPath); !os.IsNotExist(err) {
		t.Fatalf("submodule content exists without opt-in: %v", err)
	}

	options := EnsureOptions{RecurseSubmodules: true}
	if err := EnsureWithOptions(ctx, Retry{}, fixture.origin.url, dst, "", options); err != nil {
		t.Fatalf("EnsureWithOptions: %v", err)
	}
	content, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\n" {
		t.Errorf("submodule content = %q, want first revision", content)
	}
	submoduleCheckout := filepath.Join(dst, "vendor", "library")
	if got := runGitTest(t, submoduleCheckout, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("submodule shallow = %q, want true", got)
	}

	fixture.update(t, "updated\n")
	if err := EnsureWithOptions(ctx, Retry{}, fixture.origin.url, dst, "", options); err != nil {
		t.Fatalf("update checkout and submodule: %v", err)
	}
	content, err = os.ReadFile(contentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "updated\n" {
		t.Errorf("updated submodule content = %q, want updated revision", content)
	}
}

func TestEnsureWithOptionsIgnoresSubmoduleFailure(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(filepath.Join(dst, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var syncArgs []string
	var syncEnv []string
	var updateArgs []string
	var updateEnv []string
	retry := Retry{
		Attempts: 1,
		Run: func(_ context.Context, dir string, env []string, args ...string) (string, error) {
			switch subcommand(args) {
			case "fetch", "reset":
				return "", nil
			case "submodule":
				if dir != dst {
					t.Errorf("submodule dir = %q, want %q", dir, dst)
				}
				if slices.Contains(args, "sync") {
					syncArgs = append([]string(nil), args...)
					syncEnv = append([]string(nil), env...)
					return "", nil
				}
				updateArgs = append([]string(nil), args...)
				updateEnv = append([]string(nil), env...)
				return "fatal: repository not found", errGitExit
			default:
				return "", errors.New("unexpected Git command")
			}
		},
	}

	options := EnsureOptions{RecurseSubmodules: true}
	if err := EnsureWithOptions(
		context.Background(), retry, "https://example.invalid/repo", dst, "", options,
	); err != nil {
		t.Fatalf("EnsureWithOptions: %v", err)
	}
	wantSyncArgs := []string{"submodule", "sync", "--recursive"}
	if !slices.Equal(syncArgs, wantSyncArgs) {
		t.Errorf("submodule sync args = %v, want %v", syncArgs, wantSyncArgs)
	}
	wantUpdateArgs := []string{"submodule", "update", "--init", "--recursive", "--depth", "1"}
	if !slices.Equal(updateArgs, wantUpdateArgs) {
		t.Errorf("submodule update args = %v, want %v", updateArgs, wantUpdateArgs)
	}
	for name, env := range map[string][]string{"sync": syncEnv, "update": updateEnv} {
		if !slices.Contains(env, "GIT_PROTOCOL_FROM_USER=0") {
			t.Errorf("submodule %s env = %v", name, env)
		}
	}
}

func TestEnsureWithOptionsReturnsSubmoduleCancellation(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(filepath.Join(dst, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	retry := Retry{
		Attempts: 1,
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			if subcommand(args) == "submodule" {
				cancel()
				return "submodule canceled", errGitExit
			}
			return "", nil
		},
	}
	options := EnsureOptions{RecurseSubmodules: true}
	err := EnsureWithOptions(ctx, retry, "https://example.invalid/repo", dst, "", options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var unreachable *UnreachableError
	if errors.As(err, &unreachable) {
		t.Fatalf("cancellation wrapped as UnreachableError: %v", err)
	}
}

func TestEnsureRejectsInputBeforeRunningGit(t *testing.T) {
	for _, test := range []struct {
		url string
		ref string
	}{
		{"ssh://example.com/repo", "main"},
		{"https://example.com/repo", "--all"},
	} {
		calls := 0
		retry := Retry{
			Run: func(context.Context, string, []string, ...string) (string, error) {
				calls++
				return "", nil
			},
		}
		err := Ensure(context.Background(), retry, test.url, t.TempDir(), test.ref, false)
		if err == nil {
			t.Fatalf("Ensure(%q, %q) succeeded", test.url, test.ref)
		}
		var unreachable *UnreachableError
		if !errors.As(err, &unreachable) {
			t.Fatalf("error %T = %v, want *UnreachableError", err, err)
		}
		if calls != 0 {
			t.Errorf("Git calls = %d, want 0", calls)
		}
	}
}

func TestEnsureReturnsContextErrorDirectly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			cancel()
			return "fatal: the remote end hung up unexpectedly", errGitExit
		},
	}
	err := Ensure(ctx, retry, "https://example.invalid/repo", filepath.Join(t.TempDir(), "dst"), "", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var unreachable *UnreachableError
	if errors.As(err, &unreachable) {
		t.Fatalf("cancellation wrapped as UnreachableError: %v", err)
	}
}

func TestEnsureRetriesCloneAndResetsPartialDestination(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "checkout")
	calls := 0
	var envs [][]string
	var argSets [][]string
	retry := Retry{
		Run: func(_ context.Context, _ string, env []string, args ...string) (string, error) {
			calls++
			envs = append(envs, append([]string(nil), env...))
			argSets = append(argSets, append([]string(nil), args...))
			if calls == 1 {
				if err := os.MkdirAll(dst, dirPerm); err != nil {
					return "", err
				}
				if err := os.WriteFile(filepath.Join(dst, "partial"), []byte("partial"), 0o644); err != nil {
					return "", err
				}
				return "fatal: expected flush after ref listing", errGitExit
			}
			return "", nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	if err := Ensure(context.Background(), retry, "https://example.invalid/repo", dst, "", false); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("clone calls = %d, want 2", calls)
	}
	if _, err := os.Stat(filepath.Join(dst, "partial")); !os.IsNotExist(err) {
		t.Errorf("partial clone remains: %v", err)
	}
	for i, env := range envs {
		if !slices.Contains(env, "GIT_PROTOCOL_FROM_USER=0") {
			t.Errorf("attempt %d env = %v", i+1, env)
		}
	}
	for i, args := range argSets {
		wantSuffix := []string{"--", "https://example.invalid/repo", dst}
		if len(args) < len(wantSuffix) || !slices.Equal(args[len(args)-len(wantSuffix):], wantSuffix) {
			t.Errorf("attempt %d args = %v", i+1, args)
		}
	}
}

func TestEnsureConfiguresLongPathsForCloneAndFetch(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "checkout")
	commands := make(map[string][]string)
	retry := Retry{
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			commands[subcommand(args)] = append([]string(nil), args...)
			return "", nil
		},
	}

	err := Ensure(context.Background(), retry, "https://example.invalid/repo", dst, "main", false)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, command := range []string{"clone", "fetch"} {
		args := commands[command]
		want := []string{"-c", "core.longpaths=true"}
		if len(args) < len(want) || !slices.Equal(args[:len(want)], want) {
			t.Errorf("%s args = %v, want prefix %v", command, args, want)
		}
	}
}

func TestEnsureUnknownRefReturnsUnreachableError(t *testing.T) {
	origin := newOriginFixture(t)
	dst := filepath.Join(t.TempDir(), "checkout")
	err := Ensure(context.Background(), Retry{}, origin.url, dst, "missing", false)
	if err == nil {
		t.Fatal("Ensure succeeded for missing ref")
	}
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) {
		t.Fatalf("error %T = %v, want *UnreachableError", err, err)
	}
	if unreachable.URL != origin.url || !strings.Contains(err.Error(), "remote ref") {
		t.Errorf("error = %v", err)
	}
}

func TestHeadReturnsEmptyOutsideRepository(t *testing.T) {
	if got := Head(context.Background(), t.TempDir()); got != "" {
		t.Errorf("Head = %q, want empty", got)
	}
}
