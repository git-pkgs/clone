//go:build js && wasm

package clone

// js/wasm has no FIFO to block on, so a plain read-only open is enough.
const nonblockFlag = 0
