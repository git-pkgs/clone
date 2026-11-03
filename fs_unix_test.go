//go:build !windows

package clone

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO is neither a directory, a symlink nor a regular file; opening it
// would block, so the walk refuses it instead.
func TestCopyTreeRefusesNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no FIFO or device node can be planted on windows")
	}
	src := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CopyTree(context.Background(), src, filepath.Join(t.TempDir(), "copy"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("CopyTree error = %v, want a non-regular-file refusal", err)
	}
}
