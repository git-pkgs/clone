package clone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
)

// Blob reads path from commit in dir. It caps content at maxBytes and reports
// whether the blob is binary or was truncated. commit and path are validated
// with ValidCommit and SanitizePath before reaching Git.
func Blob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) (content []byte, binary, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, false, false, fmt.Errorf("maxBytes must be non-negative")
	}
	if maxBytes == math.MaxInt64 {
		return nil, false, false, fmt.Errorf("maxBytes is too large")
	}
	if !ValidCommit(commit) {
		return nil, false, false, fmt.Errorf("invalid commit %q", commit)
	}
	clean, ok := SanitizePath(blobPath)
	if !ok {
		return nil, false, false, fmt.Errorf("invalid path %q", blobPath)
	}

	// --end-of-options stops a commit or path that somehow slipped past the
	// validators from being parsed as a git-show flag. commit is validated to
	// hex above, so this is defence in depth rather than the primary guard.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", "--end-of-options", commit+":"+clean)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, false, false, err
	}

	raw, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	truncated = int64(len(raw)) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
		// Close the pipe rather than draining it: a hostile repo with a
		// multi-GB blob at this path would otherwise keep the caller in
		// io.Copy for as long as git can produce bytes. git receives EPIPE
		// or SIGPIPE and exits non-zero, which is treated as success below
		// since maxBytes was already read.
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	if waitErr != nil && !truncated {
		message := strings.TrimSpace(errBuf.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, false, false, errors.New(message)
	}
	if readErr != nil {
		return nil, false, false, readErr
	}
	if bytes.IndexByte(raw, 0) != -1 {
		return nil, true, truncated, nil
	}
	return raw, false, truncated, nil
}
