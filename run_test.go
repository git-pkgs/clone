package clone

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunUsesDirectoryAndEnvironment(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	out, err := Run(context.Background(), dir, []string{"CLONE_RUN_TEST=value"},
		"-c", "alias.test=!pwd && printf \"|%s\" \"$CLONE_RUN_TEST\"", "test")
	if err != nil {
		t.Fatalf("Run: %s: %v", out, err)
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 ||
		strings.TrimSpace(parts[0]) != resolvedDir ||
		strings.TrimSpace(parts[1]) != "value" {
		t.Fatalf("Run output = %q, want %q and environment value", out, dir)
	}
}

func TestRunnerWithWaitDelayBoundsLingeringChild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs a POSIX shell")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 30 &\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	done := make(chan struct{})
	go func() {
		_, _ = RunnerWithWaitDelay(200*time.Millisecond)(
			context.Background(), "", nil, "clone", "https://example.invalid/repo")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner blocked on a lingering transport child")
	}
}
