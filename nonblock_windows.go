//go:build windows

package clone

// Windows has no FIFO to block on, so a plain read-only open is enough.
const nonblockFlag = 0
