//go:build !unix

package clone

// No O_NONBLOCK here; the post-open stat still refuses non-regular files.
const nonblockFlag = 0
