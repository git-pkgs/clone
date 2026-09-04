package clone

import (
	"context"
	"os"
	"os/exec"
	"time"
)

const DefaultWaitDelay = 10 * time.Second

// remoteEnv is the hardening env every remote-touching Git invocation in this
// package sets. GIT_ALLOW_PROTOCOL is a hard whitelist that overrides
// protocol.*.allow config, so an ambient url.<base>.insteadOf that rewrites an
// https:// URL to file://, ssh://, or ext:: is refused after ValidateURL has
// already approved the input. A caller that needs another protocol (or a test
// using file:// via insteadOf) sets GIT_ALLOW_PROTOCOL in its own env; the
// value here defers to that. GIT_PROTOCOL_FROM_USER=0 is kept for older Git
// that predates GIT_ALLOW_PROTOCOL.
func remoteEnv() []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PROTOCOL_FROM_USER=0",
	}
	if os.Getenv("GIT_ALLOW_PROTOCOL") == "" {
		env = append(env, "GIT_ALLOW_PROTOCOL=https")
	}
	return env
}

func longPathArgs(args ...string) []string {
	return append([]string{"-c", "core.longpaths=true"}, args...)
}

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
