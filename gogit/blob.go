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
	clean, err := validateBlobRequest(ctx, commit, blobPath, maxBytes)
	if err != nil {
		return clone.BlobResult{}, err
	}
	content, truncated, goGitErr := readBlobWithGoGit(ctx, dir, commit, clean, maxBytes)
	if goGitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return clone.BlobResult{}, ctxErr
		}
		result, gitErr := clone.InspectBlob(ctx, dir, commit, clean, maxBytes)
		if gitErr != nil {
			return clone.BlobResult{}, combineBlobReadErrors(goGitErr, gitErr)
		}
		return result, nil
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
	clean, err := validateBlobRequest(ctx, commit, blobPath, maxBytes)
	if err != nil {
		return nil, false, false, err
	}
	content, truncated, goGitErr := readBlobWithGoGit(ctx, dir, commit, clean, maxBytes)
	if goGitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, false, ctxErr
		}
		content, binary, truncated, gitErr := clone.Blob(ctx, dir, commit, clean, maxBytes)
		if gitErr != nil {
			return nil, false, false, combineBlobReadErrors(goGitErr, gitErr)
		}
		return content, binary, truncated, nil
	}
	if bytes.IndexByte(content, 0) != -1 {
		return nil, true, truncated, nil
	}
	return content, false, truncated, nil
}

func validateBlobRequest(ctx context.Context, commit, blobPath string, maxBytes int64) (string, error) {
	if maxBytes < 0 {
		return "", fmt.Errorf("maxBytes must be non-negative")
	}
	if maxBytes == math.MaxInt64 {
		return "", fmt.Errorf("maxBytes is too large")
	}
	if !clone.ValidCommit(commit) {
		return "", fmt.Errorf("invalid commit %q", commit)
	}
	clean, ok := clone.SanitizePath(blobPath)
	if !ok {
		return "", fmt.Errorf("invalid path %q", blobPath)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return clean, nil
}

func combineBlobReadErrors(goGitErr, gitErr error) error {
	return errors.Join(
		fmt.Errorf("go-git blob read: %w", goGitErr),
		fmt.Errorf("git blob read: %w", gitErr),
	)
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
