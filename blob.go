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

	"github.com/git-pkgs/magic"
)

// BlobResult contains a bounded blob read and its content classification.
type BlobResult struct {
	Content   []byte
	Detection magic.Result
	Truncated bool
}

// InspectBlob reads path from commit in dir and classifies the returned bytes.
// It uses prefix detection when maxBytes truncates the blob. commit and path
// are validated with ValidCommit and SanitizePath before reading the
// repository.
func InspectBlob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) (BlobResult, error) {
	content, truncated, err := readBlob(ctx, dir, commit, blobPath, maxBytes)
	if err != nil {
		return BlobResult{}, err
	}

	var detection magic.Result
	if truncated {
		detection = magic.DetectPrefix(content)
	} else {
		detection = magic.Detect(content)
	}

	return BlobResult{
		Content:   content,
		Detection: detection,
		Truncated: truncated,
	}, nil
}

// Blob reads path from commit in dir. It caps content at maxBytes and reports
// whether the blob is binary or was truncated. commit and path are validated
// with ValidCommit and SanitizePath before reading the repository.
func Blob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) (content []byte, binary, truncated bool, err error) {
	content, truncated, err = readBlob(ctx, dir, commit, blobPath, maxBytes)
	if err != nil {
		return nil, false, false, err
	}
	if bytes.IndexByte(content, 0) != -1 {
		return nil, true, truncated, nil
	}
	return content, false, truncated, nil
}

func readBlob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) (content []byte, truncated bool, err error) {
	if maxBytes < 0 {
		return nil, false, fmt.Errorf("maxBytes must be non-negative")
	}
	if maxBytes == math.MaxInt64 {
		return nil, false, fmt.Errorf("maxBytes is too large")
	}
	if !ValidCommit(commit) {
		return nil, false, fmt.Errorf("invalid commit %q", commit)
	}
	clean, ok := SanitizePath(blobPath)
	if !ok {
		return nil, false, fmt.Errorf("invalid path %q", blobPath)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	raw, truncated, goGitErr := readBlobWithGoGit(ctx, dir, commit, clean, maxBytes)
	if goGitErr == nil {
		return raw, truncated, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	// Native Git remains the compatibility path for object formats and
	// repository layouts that go-git v5 cannot read. This also covers
	// abbreviated SHA-256 object IDs, whose length alone does not identify the
	// repository's object format.
	raw, truncated, gitErr := readBlobWithGit(ctx, dir, commit, clean, maxBytes)
	if gitErr != nil {
		return nil, false, errors.Join(
			fmt.Errorf("go-git blob read: %w", goGitErr),
			fmt.Errorf("git blob read: %w", gitErr),
		)
	}
	return raw, truncated, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		err = r.ctx.Err()
	}
	return n, err
}

func readBlobWithGit(ctx context.Context, dir, commit, clean string, maxBytes int64) (content []byte, truncated bool, err error) {
	// --end-of-options stops a commit or path that somehow slipped past the
	// validators from being parsed as a git-show flag. commit is validated to
	// hex above, so this is defence in depth rather than the primary guard.
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", "--end-of-options", commit+":"+clean)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, false, err
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
		return nil, false, errors.New(message)
	}
	if readErr != nil {
		return nil, false, readErr
	}
	return raw, truncated, nil
}
