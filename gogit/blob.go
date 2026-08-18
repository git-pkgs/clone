package gogit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/magic"
)

// InspectBlob reads path from commit through go-git and classifies the
// returned bytes. It falls back to the git binary when go-git cannot read the
// repository.
func InspectBlob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) (clone.BlobResult, error) {
	content, truncated, err := readBlob(ctx, dir, commit, blobPath, maxBytes)
	if err != nil {
		return clone.BlobResult{}, err
	}

	var detection magic.Result
	if truncated {
		detection = magic.DetectPrefix(content)
	} else {
		detection = magic.Detect(content)
	}

	return clone.BlobResult{
		Content:   content,
		Detection: detection,
		Truncated: truncated,
	}, nil
}

// Blob reads path from commit through go-git. It caps content at maxBytes and
// reports whether the blob is binary or was truncated. It falls back to the
// git binary when go-git cannot read the repository.
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

func readBlob(ctx context.Context, dir, commit, blobPath string, maxBytes int64) ([]byte, bool, error) {
	if maxBytes < 0 {
		return nil, false, fmt.Errorf("maxBytes must be non-negative")
	}
	if maxBytes == math.MaxInt64 {
		return nil, false, fmt.Errorf("maxBytes is too large")
	}
	if !clone.ValidCommit(commit) {
		return nil, false, fmt.Errorf("invalid commit %q", commit)
	}
	clean, ok := clone.SanitizePath(blobPath)
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

	result, gitErr := clone.InspectBlob(ctx, dir, commit, clean, maxBytes)
	if gitErr != nil {
		return nil, false, errors.Join(
			fmt.Errorf("go-git blob read: %w", goGitErr),
			fmt.Errorf("git blob read: %w", gitErr),
		)
	}
	return result.Content, result.Truncated, nil
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
