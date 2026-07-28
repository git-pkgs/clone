package clone

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

const (
	DefaultAttempts  = 3
	DefaultBaseDelay = 500 * time.Millisecond
	DefaultMaxDelay  = 4 * time.Second
)

// Notice describes a transient failure before the next attempt.
type Notice struct {
	Label    string
	Attempt  int
	Attempts int
	Delay    time.Duration
}

// Command is one remote Git invocation plus operation-specific hooks.
type Command struct {
	Label string
	Dir   string
	Env   []string
	Args  []string
	// Reset runs after a failed attempt, before either another attempt or a
	// terminal error return. It must only clean command-owned state.
	Reset func() error
	// Confirm may recognize that an operation succeeded despite an ambiguous
	// transient error. A confirmation error does not replace the original Git
	// error; the normal retry budget continues unless the context ended.
	Confirm func(context.Context) (bool, error)
}

// Retry bounds how a remote Git invocation is retried. Its zero value uses
// the default policy. Fields are exposed so callers can use a tighter budget
// or deterministic runners and sleepers in tests.
type Retry struct {
	Attempts  int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Run       Runner
	Sleep     func(context.Context, time.Duration) error
	Notify    func(Notice)
}

// Resolved fills zero-valued options with the defaults.
func (r Retry) Resolved() Retry {
	if r.Attempts <= 0 {
		r.Attempts = DefaultAttempts
	}
	if r.BaseDelay <= 0 {
		r.BaseDelay = DefaultBaseDelay
	}
	if r.MaxDelay <= 0 {
		r.MaxDelay = DefaultMaxDelay
	}
	if r.Run == nil {
		r.Run = Run
	}
	if r.Sleep == nil {
		r.Sleep = sleep
	}
	return r
}

// Do runs cmd, retrying only transient failures while the budget, context,
// cleanup hook, and optional success confirmation allow another attempt.
func (r Retry) Do(ctx context.Context, cmd Command) (string, error) {
	policy := r.Resolved()
	finishFailure := func(out string, err error) (string, error) {
		if cmd.Reset == nil {
			return out, err
		}
		if resetErr := cmd.Reset(); resetErr != nil {
			return out, errors.Join(err, resetErr)
		}
		return out, err
	}

	for attempt := 1; ; attempt++ {
		out, err := policy.Run(ctx, cmd.Dir, cmd.Env, cmd.Args...)
		if err == nil {
			return out, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return finishFailure(out, ctxErr)
		}
		if !TransientFailure(out) {
			return finishFailure(out, err)
		}
		if cmd.Confirm != nil {
			confirmed, _ := cmd.Confirm(ctx)
			if confirmed {
				return out, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return finishFailure(out, ctxErr)
			}
		}
		if attempt >= policy.Attempts {
			return finishFailure(out, err)
		}
		if cmd.Reset != nil {
			if resetErr := cmd.Reset(); resetErr != nil {
				return out, errors.Join(err, resetErr)
			}
		}
		delay := backoffDelay(attempt, policy.BaseDelay, policy.MaxDelay)
		if policy.Notify != nil {
			policy.Notify(Notice{
				Label:    cmd.Label,
				Attempt:  attempt,
				Attempts: policy.Attempts,
				Delay:    delay,
			})
		}
		if err := policy.Sleep(ctx, delay); err != nil {
			return out, err
		}
	}
}

// DestReset returns a cleanup function for a failed clone attempt, or nil
// when dst already contains caller-owned files.
func DestReset(dst string) func() error {
	entries, err := os.ReadDir(dst)
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	if len(entries) > 0 {
		return nil
	}
	return func() error { return os.RemoveAll(dst) }
}

var permanentFailures = []string{
	"repository not found",
	"authentication failed",
	"could not read username",
	"could not read password",
	"terminal prompts disabled",
	"permission denied (publickey)",
	"remote: permission denied",
	"access denied",
	"couldn't find remote ref",
	"does not appear to be a git repository",
	"unable to update url base from redirection",
	"returned error: 401",
	"returned error: 403",
	"returned error: 404",
	"returned error: 410",
	"returned error: 413",
	"http code = 401",
	"http code = 403",
	"http code = 404",
	"http code = 413",
	"pack exceeds maximum allowed size",
	"already exists and is not an empty directory",
	"no space left on device",
	"disk quota exceeded",
	"input/output error",
	"read-only file system",
	"cannot allocate memory",
	"unable to create thread",
	"cannot fork",
	"unable to fork",
}

var transientFailures = []string{
	"could not resolve host",
	"couldn't resolve host",
	"could not resolve proxy",
	"temporary failure in name resolution",
	"failed to connect",
	"connection refused",
	"connection reset",
	"connection timed out",
	"operation timed out",
	"timeout was reached",
	"network is unreachable",
	"no route to host",
	"the remote end hung up unexpectedly",
	"early eof",
	"rpc failed",
	"unexpected disconnect while reading sideband packet",
	"transfer closed with",
	"recv failure",
	"send failure",
	"empty reply from server",
	"gnutls_handshake() failed",
	"ssl connect error",
	"ssl_read",
	"ssl_write",
	"tls connection was non-properly terminated",
	"returned error: 408",
	"returned error: 429",
	"returned error: 500",
	"returned error: 502",
	"returned error: 503",
	"returned error: 504",
	"returned error: 520",
	"returned error: 521",
	"returned error: 522",
	"returned error: 523",
	"returned error: 524",
	"http code = 500",
	"http code = 502",
	"http code = 503",
	"http code = 504",
	"http code = 520",
	"http code = 521",
	"http code = 522",
	"http code = 523",
	"http code = 524",
	"internal server error",
	"bad gateway",
	"service unavailable",
	"service temporarily unavailable",
	"too many requests",
}

// TransientFailure reports whether Git's combined output describes a failure
// worth another attempt. Permanent markers take precedence, and unrecognized
// output is treated as permanent.
func TransientFailure(out string) bool {
	lower := strings.ToLower(out)
	for _, marker := range permanentFailures {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, marker := range transientFailures {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
