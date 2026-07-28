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
// whether the blob is binary or was truncated. Callers should validate commit
// with ValidCommit and path with SanitizePath before calling Blob.
func Blob(ctx context.Context, dir, commit, path string, maxBytes int64) (content []byte, binary, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, false, false, fmt.Errorf("maxBytes must be non-negative")
	}
	if maxBytes == math.MaxInt64 {
		return nil, false, false, fmt.Errorf("maxBytes is too large")
	}

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", commit+":"+path)
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
	if int64(len(raw)) > maxBytes {
		_, _ = io.Copy(io.Discard, stdout)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		message := strings.TrimSpace(errBuf.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, false, false, errors.New(message)
	}
	if readErr != nil {
		return nil, false, false, readErr
	}
	if int64(len(raw)) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}
	if bytes.IndexByte(raw, 0) != -1 {
		return nil, true, truncated, nil
	}
	return raw, false, truncated, nil
}
