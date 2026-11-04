//go:build !windows

package clone

import "syscall"

// nonblockFlag keeps a FIFO swapped in after the walk listed a regular file
// from blocking Open, so the post-open stat can refuse it.
const nonblockFlag = syscall.O_NONBLOCK
