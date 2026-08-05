package clone

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const refusalOutput = "fatal: transport 'ssh' not allowed\n"

func refusedOnce(calls *[][]string, out string) Runner {
	return func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
		*calls = append(*calls, append([]string(nil), args...))
		if len(*calls) == 1 {
			return refusalOutput, errors.New("exit status 128")
		}
		return out, nil
	}
}

// An ambient url.<base>.insteadOf in the user's Git config rewrites a URL that
// ValidateURL already approved, and GIT_ALLOW_PROTOCOL then refuses the
// transport it landed on. The retry pins the URL to itself so the validated
// URL is the one Git contacts.
func TestRemoteBranchesRetriesWithPinnedURLAfterTransportRefusal(t *testing.T) {
	const url = "https://example.com/repo"
	var calls [][]string
	retry := Retry{Run: refusedOnce(&calls, "bbb\trefs/heads/z\naaa\trefs/heads/main\n")}

	branches, err := RemoteBranches(context.Background(), retry, url)
	if err != nil {
		t.Fatalf("RemoteBranches: %v", err)
	}
	if !slices.Equal(branches, []string{"main", "z"}) {
		t.Errorf("branches = %v, want [main z]", branches)
	}
	if len(calls) != 2 {
		t.Fatalf("git invocations = %d, want 2", len(calls))
	}
	unpinned := []string{"-c", "credential.helper=", "ls-remote", "--heads", "--", url}
	if !slices.Equal(calls[0], unpinned) {
		t.Errorf("first attempt = %v, want %v", calls[0], unpinned)
	}
	if want := append(pinArgs(url), unpinned...); !slices.Equal(calls[1], want) {
		t.Errorf("retry = %v, want %v", calls[1], want)
	}
}

func TestRemoteHeadRetriesWithPinnedURLAfterTransportRefusal(t *testing.T) {
	const url = "https://example.com/repo"
	var calls [][]string
	retry := Retry{Run: refusedOnce(&calls, "deadbeef\tHEAD\n")}

	head, err := RemoteHead(context.Background(), retry, url)
	if err != nil {
		t.Fatalf("RemoteHead: %v", err)
	}
	if head != "deadbeef" {
		t.Errorf("head = %q, want deadbeef", head)
	}
	if len(calls) != 2 {
		t.Fatalf("git invocations = %d, want 2", len(calls))
	}
	if want := append(pinArgs(url), calls[0]...); !slices.Equal(calls[1], want) {
		t.Errorf("retry = %v, want %v", calls[1], want)
	}
}

// Ensure clones through the same helper, so a refusal on the clone itself is
// recovered rather than surfacing as an UnreachableError.
func TestEnsureRetriesWithPinnedURLAfterTransportRefusal(t *testing.T) {
	const url = "https://example.com/repo"
	dst := filepath.Join(t.TempDir(), "checkout")
	var calls [][]string
	retry := Retry{Run: refusedOnce(&calls, "")}

	if err := Ensure(context.Background(), retry, url, dst, "", false); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("git invocations = %d, want 2", len(calls))
	}
	if !slices.Equal(calls[1], append(pinArgs(url), calls[0]...)) {
		t.Errorf("retry = %v, want the first attempt with %v prepended", calls[1], pinArgs(url))
	}
}

// The pin must not appear on a command Git accepted. An insteadOf rewrite
// between two https:// URLs -- the usual internal-mirror setup -- is never
// refused, so pinning it would break a working configuration to fix nothing.
func TestSuccessfulCommandIsNeverPinned(t *testing.T) {
	const url = "https://example.com/repo"
	var calls [][]string
	retry := Retry{
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			calls = append(calls, append([]string(nil), args...))
			return "aaa\trefs/heads/main\n", nil
		},
	}
	if _, err := RemoteBranches(context.Background(), retry, url); err != nil {
		t.Fatalf("RemoteBranches: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("git invocations = %d, want 1", len(calls))
	}
	if slices.Contains(calls[0], pinArgs(url)[1]) {
		t.Errorf("args = %v, want no insteadOf pin", calls[0])
	}
}

// A failure Git did not describe as a refused transport is not something the
// pin can fix, so it must not buy the caller a second round of attempts.
func TestUnrelatedFailureIsNotRetriedWithPin(t *testing.T) {
	const url = "https://example.com/repo"
	var calls [][]string
	retry := Retry{
		Run: func(_ context.Context, _ string, _ []string, args ...string) (string, error) {
			calls = append(calls, append([]string(nil), args...))
			return "fatal: repository not found\n", errors.New("exit status 128")
		},
	}
	if _, err := RemoteBranches(context.Background(), retry, url); err == nil {
		t.Fatal("RemoteBranches succeeded, want error")
	}
	if len(calls) != 1 {
		t.Errorf("git invocations = %d, want 1", len(calls))
	}
}

func TestTransportRefused(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"ssh refusal", "fatal: transport 'ssh' not allowed", true},
		{"file refusal", "fatal: transport 'file' not allowed", true},
		{"ext refusal", "fatal: transport 'ext' not allowed", true},
		{"repository missing", "fatal: repository 'https://example.com/x' not found", false},
		{"connection refused", "fatal: unable to access: Connection refused", false},
		{"empty", "", false},
		{"quote without refusal", "fatal: transport 'ssh' is fine", false},
	}
	for _, test := range cases {
		if got := transportRefused(test.out); got != test.want {
			t.Errorf("%s: transportRefused(%q) = %v, want %v", test.name, test.out, got, test.want)
		}
	}
}

// The retry keys off Git's wording, so pin that wording against the real
// binary rather than a message invented here. Git refuses the transport before
// any network access, so this stays offline.
func TestTransportRefusedMatchesGitsRefusal(t *testing.T) {
	requireGit(t)
	const url = "https://example.invalid/owner/repo"
	// Both spellings a hand-written insteadOf commonly uses: an ssh:// URL and
	// Git's scp-like shorthand, which carries no scheme at all.
	for _, rewrite := range []string{"ssh://git@example.invalid/", "git@example.invalid:"} {
		t.Run(rewrite, func(t *testing.T) {
			cmd := exec.Command("git", "ls-remote", "--heads", "--", url)
			cmd.Env = append(ambientRewriteEnv(rewrite, "https://example.invalid/"), remoteEnv()...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("git accepted the rewritten URL: %s", out)
			}
			if !transportRefused(string(out)) {
				t.Errorf("transportRefused did not match Git's refusal: %q", out)
			}
		})
	}
}

// The fix rests on Git preferring the longest matching insteadOf, so prove
// that a whole-URL self-map outranks a prefix rule. --get-url applies the
// rewrite rules and prints the result without contacting the remote.
func TestPinnedURLOutranksAmbientPrefixRewrite(t *testing.T) {
	requireGit(t)
	const url = "https://example.invalid/owner/repo"
	env := ambientRewriteEnv("ssh://git@example.invalid/", "https://example.invalid/")

	resolve := func(args ...string) string {
		cmd := exec.Command("git", append(args, "ls-remote", "--get-url", "--", url)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}

	if got := resolve(); got == url {
		t.Fatalf("fixture did not rewrite the URL: %q", got)
	} else if !strings.HasPrefix(got, "ssh://") {
		t.Fatalf("fixture rewrote to %q, want an ssh:// URL", got)
	}
	if got := resolve(pinArgs(url)...); got != url {
		t.Errorf("pinned URL resolved to %q, want %q", got, url)
	}
}

// ambientRewriteEnv builds an environment carrying one url.<rewrite>.insteadOf
// = <match> rule, standing in for a rule in the user's ~/.gitconfig.
func ambientRewriteEnv(rewrite, match string) []string {
	return append(gitTestEnv(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url."+rewrite+".insteadOf",
		"GIT_CONFIG_VALUE_0="+match,
	)
}
