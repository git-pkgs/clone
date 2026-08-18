// Package clone keeps local checkouts of HTTPS Git repositories. It provides
// shallow clone-or-fetch, exact temporary tag checkouts, bounded retries for
// network failures, a persistent cache, and capped reads and content
// classification for files from commits. Blob reads use go-git in process.
// Clone, fetch, checkout, remote queries, and command retries shell out to the
// git binary, which must be on PATH.
package clone
