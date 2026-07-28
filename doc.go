// Package clone keeps local checkouts of HTTPS Git repositories. It shells
// out to the git binary, which must be on PATH, and has no third-party Go
// dependencies. It provides shallow clone-or-fetch, bounded retries for
// network failures, a persistent cache, and capped reads of files from
// commits.
//
// Applications that need to parse Git objects or walk history in process can
// use a library such as github.com/go-git/go-git.
package clone
