//go:build js && wasm

package clone

// js/wasm has no O_NONBLOCK: the flag value is the host OS's, which a wasm
// binary cannot know. This target keeps the plain open main has always used,
// so a FIFO swapped in after the walk can still block the open under a Node
// host; the post-open stat refuses whatever turns out to be there.
const nonblockFlag = 0
