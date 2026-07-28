# clone

Go library for programs that keep local checkouts of remote Git repositories. It clones or fetches HTTPS repositories, retries recognized network failures, maintains a persistent shallow cache, and reads files from commits without loading an entire blob into memory.

The package shells out to the `git` binary and requires Git on `PATH`. It has no third-party Go dependencies and supports Go 1.25 or later.

`clone` focuses on command-level checkout operations. Applications that need to parse Git objects or walk history in process can use a library such as [go-git](https://github.com/go-git/go-git).

## Install

```
go get github.com/git-pkgs/clone
```

## Clone or update a checkout

`Ensure` creates a shallow clone on its first call. Later calls fetch the requested ref and reset the existing checkout. The ref can be a branch, tag, commit ID, or an empty string for the remote's default branch.

```go
ctx := context.Background()
url := "https://github.com/git-pkgs/clone"
dst := "/var/cache/my-tool/clone"

if err := clone.Ensure(ctx, clone.Retry{}, url, dst, "main", false); err != nil {
    log.Fatal(err)
}

fmt.Println(clone.Head(ctx, dst))
```

Pass `true` as the final argument for a full clone. Calling `Ensure` with `true` also unshallows a checkout created by an earlier call.

Clone and fetch errors are returned as `*clone.UnreachableError`, except when the context was canceled or reached its deadline. `errors.As` can retrieve the URL and underlying Git error.

## Persistent cache

`Cache` stores one checkout per URL under `Root`. `Prepare` updates the shared shallow checkout while holding a per-URL lock, replaces `dst` with a copy, and returns the commit copied into the destination. The destination must be outside `Root`.

```go
cache := clone.Cache{
    Root: "/var/cache/my-tool/repositories",
}

commit, err := cache.Prepare(ctx,
    "https://github.com/git-pkgs/clone",
    "",
    "/tmp/job/src",
)
if err != nil {
    log.Fatal(err)
}

fmt.Println(commit, cache.DiskBytes("https://github.com/git-pkgs/clone"))
```

`EnsureCommit` unshallows the cached checkout when a historical commit is missing. This lets callers keep the usual update path shallow and pay for full history only when they need it.

```go
if err := cache.EnsureCommit(ctx, url, commit); err != nil {
    log.Fatal(err)
}
```

## Read a file from a commit

`Blob` runs `git show <commit>:<path>`, reads at most `maxBytes+1`, and drains the remaining output so Git can exit. The extra byte distinguishes content exactly at the limit from truncated content. A NUL byte within the returned range marks the blob as binary.

Validate untrusted commit IDs and paths before passing them to `Blob`:

```go
path, ok := clone.SanitizePath("cmd/tool/main.go")
if !ok || !clone.ValidCommit(commit) {
    log.Fatal("invalid commit or path")
}

content, binary, truncated, err := clone.Blob(
    ctx,
    filepath.Join(cache.Dir(url), "src"),
    commit,
    path,
    2<<20,
)
if err != nil {
    log.Fatal(err)
}
if !binary {
    fmt.Printf("%s", content)
}
fmt.Println("truncated:", truncated)
```

`ValidateURL` accepts `https://` URLs. `ValidateRef` accepts an empty ref or names made from letters, digits, `.`, `_`, `/`, and `-`, while rejecting leading hyphens and `..`.

## Remote queries

`RemoteBranches` returns sorted branch names from `git ls-remote --heads`. It disables terminal prompts and the ambient credential helper so URLs from untrusted input cannot trigger credential lookup. `RemoteHead` returns the SHA advertised for `HEAD` and keeps ambient non-interactive credentials available.

```go
branches, err := clone.RemoteBranches(ctx, clone.Retry{}, url)
head, err := clone.RemoteHead(ctx, clone.Retry{}, url)
```

## Retry policy

The zero value of `Retry` makes three attempts with exponential backoff and positive jitter. `Do` retries output recognized as a transient network or remote-service failure. Permanent markers take precedence, and unknown output is treated as permanent.

```go
retry := clone.Retry{
    Notify: func(notice clone.Notice) {
        log.Printf(
            "retrying %s after attempt %d of %d in %s",
            notice.Label,
            notice.Attempt,
            notice.Attempts,
            notice.Delay,
        )
    },
}

out, err := retry.Do(ctx, clone.Command{
    Label: "ls-remote",
    Env:   []string{"GIT_TERMINAL_PROMPT=0"},
    Args:  []string{"ls-remote", "--", url, "HEAD"},
})
```

`TransientFailure` exposes the same fail-closed classifier for callers with their own command loop. `RunnerWithWaitDelay` creates a `Runner` with a custom bound for transport child processes that retain Git's output pipe.

## License

MIT
