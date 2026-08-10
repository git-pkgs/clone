package clone

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

const goGitV5SHA1HexLength = 40

func readBlobWithGoGit(ctx context.Context, dir, commit, blobPath string, maxBytes int64) ([]byte, bool, error) {
	if len(commit) > goGitV5SHA1HexLength {
		return nil, false, fmt.Errorf("go-git v5 does not support %d-character object IDs", len(commit))
	}

	store, err := openObjectStore(dir, maxBytes)
	if err != nil {
		return nil, false, err
	}
	commitHash, err := resolveCommitHash(ctx, store, commit)
	if err != nil {
		return nil, false, err
	}
	treeHash, err := readHeaderHash(ctx, store, plumbing.CommitObject, commitHash, "tree")
	if err != nil {
		return nil, false, err
	}
	blob, err := findBlobObject(ctx, store, treeHash, blobPath)
	if err != nil {
		return nil, false, err
	}
	if blob.Size() < 0 {
		return nil, false, fmt.Errorf("blob %q has invalid size %d", blobPath, blob.Size())
	}

	readSize := min(blob.Size(), maxBytes)
	if uint64(readSize) > uint64(maxInt()) {
		return nil, false, fmt.Errorf("blob read size %d exceeds platform limit", readSize)
	}
	content := make([]byte, int(readSize))
	if readSize == 0 {
		return content, blob.Size() > maxBytes, nil
	}

	reader, err := blob.Reader()
	if err != nil {
		return nil, false, err
	}
	_, readErr := io.ReadFull(contextReader{ctx: ctx, reader: reader}, content)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return content, blob.Size() > maxBytes, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func openObjectStore(dir string, maxBytes int64) (*filesystem.Storage, error) {
	gitDir, commonDir, err := findGitDirs(dir)
	if err != nil {
		return nil, err
	}

	dotGitFS := osfs.New(gitDir)
	var repositoryFS billy.Filesystem
	if commonDir != "" && commonDir != gitDir {
		repositoryFS = dotgit.NewRepositoryFilesystem(dotGitFS, osfs.New(commonDir))
	} else {
		repositoryFS = dotGitFS
	}
	largeObjectThreshold := max(maxBytes, 1)
	return filesystem.NewStorageWithOptions(
		repositoryFS,
		cache.NewObjectLRUDefault(),
		filesystem.Options{LargeObjectThreshold: largeObjectThreshold},
	), nil
}

func findGitDirs(dir string) (gitDir, commonDir string, err error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(start)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("repository path %q is not a directory", dir)
	}

	for current := start; ; current = filepath.Dir(current) {
		dotGit := filepath.Join(current, ".git")
		if info, statErr := os.Stat(dotGit); statErr == nil {
			if info.IsDir() {
				return gitDirectories(dotGit)
			}
			resolved, resolveErr := resolveGitFile(dotGit)
			if resolveErr != nil {
				return "", "", resolveErr
			}
			return gitDirectories(resolved)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", statErr
		}

		if current == start && isBareGitDir(current) {
			return gitDirectories(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", fmt.Errorf("repository does not exist")
		}
	}
}

func resolveGitFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	const prefix = "gitdir:"
	if !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("invalid gitfile %q", path)
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	if gitDir == "" {
		return "", fmt.Errorf("invalid gitfile %q", path)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(path), gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func gitDirectories(gitDir string) (string, string, error) {
	content, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, os.ErrNotExist) {
		return gitDir, "", nil
	}
	if err != nil {
		return "", "", err
	}
	commonDir := strings.TrimSpace(string(content))
	if commonDir == "" {
		return "", "", fmt.Errorf("empty commondir in %q", gitDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return gitDir, filepath.Clean(commonDir), nil
}

func isBareGitDir(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "objects")); err != nil || !info.IsDir() {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil && !info.IsDir()
}

func resolveCommitHash(ctx context.Context, store *filesystem.Storage, revision string) (plumbing.Hash, error) {
	hashes, err := resolveObjectPrefix(store, revision)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	var resolved plumbing.Hash
	found := false
	for _, hash := range hashes {
		commit, commitErr := peelCommitHash(ctx, store, hash)
		if commitErr != nil {
			continue
		}
		if found {
			return plumbing.ZeroHash, fmt.Errorf("ambiguous commit prefix %q", revision)
		}
		resolved = commit
		found = true
	}
	if !found {
		return plumbing.ZeroHash, fmt.Errorf("commit %q not found", revision)
	}
	return resolved, nil
}

func peelCommitHash(ctx context.Context, store *filesystem.Storage, hash plumbing.Hash) (plumbing.Hash, error) {
	const maxTagDepth = 16
	for range maxTagDepth {
		encoded, err := store.EncodedObject(plumbing.AnyObject, hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		switch encoded.Type() {
		case plumbing.CommitObject:
			return hash, nil
		case plumbing.TagObject:
			hash, err = readEncodedHeaderHash(ctx, encoded, "object")
			if err != nil {
				return plumbing.ZeroHash, err
			}
		default:
			return plumbing.ZeroHash, fmt.Errorf("object %s is not a commit", hash)
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("tag chain for %s is too deep", hash)
}

func readHeaderHash(
	ctx context.Context,
	store *filesystem.Storage,
	typeName plumbing.ObjectType,
	hash plumbing.Hash,
	header string,
) (plumbing.Hash, error) {
	encoded, err := store.EncodedObject(typeName, hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return readEncodedHeaderHash(ctx, encoded, header)
}

func readEncodedHeaderHash(ctx context.Context, encoded plumbing.EncodedObject, header string) (plumbing.Hash, error) {
	reader, err := encoded.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	line, readErr := bufio.NewReader(contextReader{ctx: ctx, reader: reader}).ReadString('\n')
	closeErr := reader.Close()
	if readErr != nil {
		return plumbing.ZeroHash, readErr
	}
	if closeErr != nil {
		return plumbing.ZeroHash, closeErr
	}
	value, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), header+" ")
	if !ok || !plumbing.IsHash(value) {
		return plumbing.ZeroHash, fmt.Errorf("object has invalid %s header", header)
	}
	return plumbing.NewHash(value), nil
}

func findBlobObject(
	ctx context.Context,
	store *filesystem.Storage,
	treeHash plumbing.Hash,
	blobPath string,
) (*storedBlob, error) {
	parts := strings.Split(blobPath, "/")
	for index, part := range parts {
		tree, err := store.EncodedObject(plumbing.TreeObject, treeHash)
		if err != nil {
			return nil, err
		}
		mode, entryHash, err := findTreeEntry(ctx, tree, part)
		if err != nil {
			return nil, err
		}
		if index == len(parts)-1 {
			encoded, encodedErr := store.EncodedObject(plumbing.BlobObject, entryHash)
			if encodedErr != nil {
				return nil, encodedErr
			}
			return &storedBlob{EncodedObject: encoded}, nil
		}
		if mode != "40000" && mode != "040000" {
			return nil, fmt.Errorf("path component %q is not a directory", part)
		}
		treeHash = entryHash
	}
	return nil, fmt.Errorf("path %q does not exist", blobPath)
}

type storedBlob struct {
	plumbing.EncodedObject
}

func findTreeEntry(
	ctx context.Context,
	encoded plumbing.EncodedObject,
	want string,
) (mode string, hash plumbing.Hash, err error) {
	reader, err := encoded.Reader()
	if err != nil {
		return "", plumbing.ZeroHash, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
	}()

	buffered := bufio.NewReader(contextReader{ctx: ctx, reader: reader})
	for {
		mode, err = buffered.ReadString(' ')
		if errors.Is(err, io.EOF) {
			return "", plumbing.ZeroHash, fmt.Errorf("path %q does not exist", want)
		}
		if err != nil {
			return "", plumbing.ZeroHash, err
		}
		mode = strings.TrimSuffix(mode, " ")
		name, nameErr := buffered.ReadString(0)
		if nameErr != nil {
			return "", plumbing.ZeroHash, nameErr
		}
		name = strings.TrimSuffix(name, "\x00")
		if _, err = io.ReadFull(buffered, hash[:]); err != nil {
			return "", plumbing.ZeroHash, err
		}
		if name == want {
			return mode, hash, nil
		}
	}
}

func resolveObjectPrefix(store *filesystem.Storage, revision string) ([]plumbing.Hash, error) {
	if len(revision) == goGitV5SHA1HexLength {
		return []plumbing.Hash{plumbing.NewHash(revision)}, nil
	}

	evenHex := revision[:len(revision)&^1]
	prefix, err := hex.DecodeString(evenHex)
	if err != nil {
		return nil, err
	}
	hashes, err := store.HashesWithPrefix(prefix)
	if err != nil {
		return nil, err
	}
	if len(evenHex) == len(revision) {
		return hashes, nil
	}
	filtered := hashes[:0]
	for _, hash := range hashes {
		if strings.HasPrefix(hash.String(), revision) {
			filtered = append(filtered, hash)
		}
	}
	return filtered, nil
}
