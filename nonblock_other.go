//go:build !unix

package clone

// Non-unix targets either have no FIFOs to block on (windows) or cannot know
// the host OS's O_NONBLOCK value (js/wasm, wasip1), so the open stays plain;
// the post-open stat refuses whatever turns out to be there.
const nonblockFlag = 0
