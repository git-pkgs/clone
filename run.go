package clone

import (
	"context"
	"os"
	"os/exec"
	"time"
)

const DefaultWaitDelay = 10 * time.Second

// Runner runs one Git invocation and returns its combined output.
type Runner func(ctx context.Context, dir string, env []string, args ...string) (string, error)

// Run executes Git with the production WaitDelay.
func Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return RunnerWithWaitDelay(DefaultWaitDelay)(ctx, dir, env, args...)
}

// RunnerWithWaitDelay returns a Runner with a bounded wait for transport
// children that retain Git's output pipe after Git itself exits.
func RunnerWithWaitDelay(waitDelay time.Duration) Runner {
	return func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		cmd.WaitDelay = waitDelay
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
}
