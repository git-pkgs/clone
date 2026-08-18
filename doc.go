// Package clone keeps local checkouts of HTTPS Git repositories. It provides
// shallow clone-or-fetch, exact temporary tag checkouts, bounded retries for
// network failures, a persistent cache, and capped reads and content
// classification for files from commits. Operations shell out to the git
// binary, which must be on PATH. The github.com/git-pkgs/clone/gogit module
// provides in-process blob reads.
package clone
