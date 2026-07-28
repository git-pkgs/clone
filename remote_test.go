package clone

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseRemoteHeads(t *testing.T) {
	out := "deadbeef\trefs/heads/main\n" +
		"cafebabe\trefs/heads/7.2\n" +
		"cafebabe\trefs/heads/7.2\n" +
		"00000000\trefs/heads/6.4\n" +
		"feedface\trefs/tags/v1\n"
	want := []string{"6.4", "7.2", "main"}
	if got := parseRemoteHeads(out); !slices.Equal(got, want) {
		t.Errorf("parseRemoteHeads = %v, want %v", got, want)
	}
}

func TestRemoteBranchesUsesHardenedGitInvocation(t *testing.T) {
	var gotEnv, gotArgs []string
	retry := Retry{
		Run: func(_ context.Context, _ string, env []string, args ...string) (string, error) {
			gotEnv = append([]string(nil), env...)
			gotArgs = append([]string(nil), args...)
			return "bbb\trefs/heads/z\n" +
				"aaa\trefs/heads/main\n", nil
		},
	}
	branches, err := RemoteBranches(context.Background(), retry, "https://example.com/repo")
	if err != nil {
		t.Fatalf("RemoteBranches: %v", err)
	}
	if !slices.Equal(branches, []string{"main", "z"}) {
		t.Errorf("branches = %v", branches)
	}
	if !slices.Equal(gotEnv, []string{"GIT_TERMINAL_PROMPT=0"}) {
		t.Errorf("env = %v", gotEnv)
	}
	wantArgs := []string{
		"-c", "credential.helper=", "ls-remote", "--heads", "--", "https://example.com/repo",
	}
	if !slices.Equal(gotArgs, wantArgs) {
		t.Errorf("args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestRemoteHeadReturnsAdvertisedHead(t *testing.T) {
	retry := Retry{
		Run: func(_ context.Context, _ string, env []string, args ...string) (string, error) {
			if !slices.Equal(env, []string{"GIT_TERMINAL_PROMPT=0"}) {
				t.Errorf("env = %v", env)
			}
			want := []string{"ls-remote", "--", "https://example.com/repo", "HEAD"}
			if !slices.Equal(args, want) {
				t.Errorf("args = %v, want %v", args, want)
			}
			return "deadbeef\tHEAD\ncafebabe\trefs/heads/main\n", nil
		},
	}
	head, err := RemoteHead(context.Background(), retry, "https://example.com/repo")
	if err != nil {
		t.Fatalf("RemoteHead: %v", err)
	}
	if head != "deadbeef" {
		t.Errorf("head = %q, want deadbeef", head)
	}
}

func TestRemoteHeadRejectsMissingHead(t *testing.T) {
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			return "cafebabe\trefs/heads/main\n", nil
		},
	}
	_, err := RemoteHead(context.Background(), retry, "https://example.com/repo")
	if err == nil || !strings.Contains(err.Error(), "no HEAD") {
		t.Fatalf("error = %v, want missing HEAD error", err)
	}
}

func TestRemoteQueriesRejectURLBeforeGit(t *testing.T) {
	calls := 0
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			calls++
			return "", errors.New("must not run")
		},
	}
	if _, err := RemoteBranches(context.Background(), retry, "file:///tmp/repo"); err == nil {
		t.Fatal("RemoteBranches accepted file URL")
	}
	if _, err := RemoteHead(context.Background(), retry, "ssh://example.com/repo"); err == nil {
		t.Fatal("RemoteHead accepted SSH URL")
	}
	if calls != 0 {
		t.Errorf("Git calls = %d, want 0", calls)
	}
}

func TestRemoteQueryIncludesGitOutputInError(t *testing.T) {
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			return "fatal: repository not found\n", errGitExit
		},
	}
	_, err := RemoteBranches(context.Background(), retry, "https://example.com/missing")
	if err == nil || !strings.Contains(err.Error(), "repository not found") {
		t.Fatalf("error = %v", err)
	}
}
