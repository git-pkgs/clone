package clone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var errGitExit = errors.New("exit status 128")

func TestRetryRetriesTransientFailureAndNotifies(t *testing.T) {
	calls := 0
	var notices []Notice
	retry := Retry{
		Attempts: 2,
		Run: func(context.Context, string, []string, ...string) (string, error) {
			calls++
			if calls == 1 {
				return "fatal: the remote end hung up unexpectedly", errGitExit
			}
			return "ok", nil
		},
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Notify: func(notice Notice) { notices = append(notices, notice) },
	}

	out, err := retry.Do(context.Background(), Command{Label: "fetch", Args: []string{"fetch"}})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != "ok" || calls != 2 {
		t.Fatalf("Do returned %q after %d calls, want ok after 2", out, calls)
	}
	if len(notices) != 1 {
		t.Fatalf("notices = %v, want one", notices)
	}
	notice := notices[0]
	if notice.Label != "fetch" || notice.Attempt != 1 || notice.Attempts != 2 {
		t.Errorf("notice = %+v", notice)
	}
	if notice.Delay < DefaultBaseDelay || notice.Delay >= DefaultBaseDelay+DefaultBaseDelay/jitterDivisor {
		t.Errorf("notice delay = %v, want first backoff near %v", notice.Delay, DefaultBaseDelay)
	}
}

func TestRetryConfirmRecognizesAmbiguousSuccess(t *testing.T) {
	calls := 0
	confirmations := 0
	retry := Retry{
		Attempts: 2,
		Run: func(context.Context, string, []string, ...string) (string, error) {
			calls++
			return "fatal: the remote end hung up unexpectedly", errGitExit
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	_, err := retry.Do(context.Background(), Command{
		Label: "push",
		Args:  []string{"push"},
		Confirm: func(context.Context) (bool, error) {
			confirmations++
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 1 || confirmations != 1 {
		t.Fatalf("calls = %d, confirmations = %d, want 1 each", calls, confirmations)
	}
}

func TestRetryConfirmationErrorFallsBackToRetry(t *testing.T) {
	calls := 0
	retry := Retry{
		Attempts: 2,
		Run: func(context.Context, string, []string, ...string) (string, error) {
			calls++
			if calls == 1 {
				return "fatal: the remote end hung up unexpectedly", errGitExit
			}
			return "", nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	_, err := retry.Do(context.Background(), Command{
		Label: "push",
		Confirm: func(context.Context) (bool, error) {
			return false, errors.New("confirmation unavailable")
		},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestRetryResetsAfterEveryFailure(t *testing.T) {
	resets := 0
	retry := Retry{
		Attempts: 2,
		Run: func(context.Context, string, []string, ...string) (string, error) {
			return "fatal: the remote end hung up unexpectedly", errGitExit
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	_, err := retry.Do(context.Background(), Command{
		Label: "clone",
		Reset: func() error {
			resets++
			return nil
		},
	})
	if !errors.Is(err, errGitExit) {
		t.Fatalf("error = %v, want Git error", err)
	}
	if resets != 2 {
		t.Fatalf("resets = %d, want 2", resets)
	}
}

func TestRetryDoesNotRetryPermanentFailure(t *testing.T) {
	calls := 0
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			calls++
			return "remote: Repository not found.", errGitExit
		},
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("permanent failure must not sleep")
			return nil
		},
	}
	_, err := retry.Do(context.Background(), Command{Label: "clone"})
	if !errors.Is(err, errGitExit) {
		t.Fatalf("error = %v, want Git error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryReturnsCancellationAndJoinsResetError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resetErr := errors.New("cleanup failed")
	retry := Retry{
		Run: func(context.Context, string, []string, ...string) (string, error) {
			cancel()
			return "fatal: the remote end hung up unexpectedly", errGitExit
		},
	}
	_, err := retry.Do(ctx, Command{Reset: func() error { return resetErr }})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, resetErr) {
		t.Fatalf("error = %v, want cancellation and reset error", err)
	}
}

func TestRetryResolvedUsesDefaults(t *testing.T) {
	resolved := (Retry{}).Resolved()
	if resolved.Attempts != DefaultAttempts ||
		resolved.BaseDelay != DefaultBaseDelay ||
		resolved.MaxDelay != DefaultMaxDelay ||
		resolved.Run == nil ||
		resolved.Sleep == nil {
		t.Errorf("resolved defaults = %+v", resolved)
	}
}

func TestDestResetOnlyRemovesEmptyOrAbsentDestination(t *testing.T) {
	occupied := t.TempDir()
	keep := filepath.Join(occupied, "keep")
	if err := os.WriteFile(keep, []byte("caller content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reset := DestReset(occupied); reset != nil {
		t.Fatal("DestReset returned cleanup for occupied destination")
	}

	empty := t.TempDir()
	reset := DestReset(empty)
	if reset == nil {
		t.Fatal("DestReset returned nil for empty destination")
	}
	if err := reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Errorf("empty destination still exists: %v", err)
	}

	absent := filepath.Join(t.TempDir(), "absent")
	if reset := DestReset(absent); reset == nil {
		t.Error("DestReset returned nil for absent destination")
	}
}

func TestTransientFailure(t *testing.T) {
	transient := []string{
		"fatal: unable to access remote: Could not resolve host: example.invalid",
		"fatal: unable to access remote: Connection refused",
		"fatal: the remote end hung up unexpectedly",
		"fatal: expected flush after ref listing",
		"error: RPC failed; HTTP 503 curl 22 Service Temporarily Unavailable",
		"fatal: unable to access remote: The requested URL returned error: 429",
		"fatal: unable to access remote: The requested URL returned error: 524",
	}
	for _, out := range transient {
		if !TransientFailure(out) {
			t.Errorf("TransientFailure(%q) = false", out)
		}
	}

	permanent := []string{
		"",
		"remote: Repository not found.",
		"fatal: Authentication failed",
		"fatal: couldn't find remote ref missing",
		"fatal: write error: No space left on device",
		"fatal: an unclassified failure",
	}
	for _, out := range permanent {
		if TransientFailure(out) {
			t.Errorf("TransientFailure(%q) = true", out)
		}
	}
}

func TestTransientFailurePermanentMarkerWins(t *testing.T) {
	cases := []string{
		"Connection reset by peer\nremote: Repository not found.",
		"No space left on device\nfatal: the remote end hung up unexpectedly",
		"HTTP code = 401\nfatal: the remote end hung up unexpectedly",
	}
	for _, out := range cases {
		if TransientFailure(out) {
			t.Errorf("mixed output classified as transient: %q", out)
		}
	}
}

func TestRetryPassesCommandFieldsToRunner(t *testing.T) {
	var gotDir string
	var gotEnv, gotArgs []string
	retry := Retry{
		Run: func(_ context.Context, dir string, env []string, args ...string) (string, error) {
			gotDir = dir
			gotEnv = append([]string(nil), env...)
			gotArgs = append([]string(nil), args...)
			return "", nil
		},
	}
	command := Command{
		Dir:  "/tmp/repo",
		Env:  []string{"GIT_TERMINAL_PROMPT=0"},
		Args: []string{"fetch", "--", "origin"},
	}
	if _, err := retry.Do(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if gotDir != command.Dir || !slices.Equal(gotEnv, command.Env) || !slices.Equal(gotArgs, command.Args) {
		t.Errorf("runner received dir=%q env=%v args=%v", gotDir, gotEnv, gotArgs)
	}
}
