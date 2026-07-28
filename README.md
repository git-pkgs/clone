# clone

Go library for programs that keep local checkouts of HTTPS Git repositories. It shells out to the `git` binary, which must be on `PATH`, and has no third-party Go dependencies. The package supports Go 1.25 or later. For in-process object parsing or history walking, use a library such as [go-git](https://github.com/go-git/go-git).

## Install

```
go get github.com/git-pkgs/clone
```

## Clone or update a checkout

`Ensure` creates a shallow clone on its first call, then fetches and resets the existing checkout on later calls. The ref can be a branch, tag, commit ID, or an empty string for the remote's default branch.

```go
ctx := context.Background()
url := "https://github.com/git-pkgs/clone"
dst := "/var/cache/my-tool/clone"

if err := clone.Ensure(ctx, clone.Retry{}, url, dst, "main", false); err != nil {
    log.Fatal(err)
}

fmt.Println(clone.Head(ctx, dst))
```

Pass `true` as the final argument for a full clone, including when an existing shallow checkout needs to be unshallowed. Clone and fetch errors are returned as `*clone.UnreachableError`, except when the context was canceled or reached its deadline. `errors.As` retrieves the URL and underlying Git error. `ValidateURL` accepts `https://` URLs, while `ValidateRef` rejects leading hyphens, `..`, and characters outside letters, digits, `.`, `_`, `/`, and `-`.

## Persistent cache

`Cache` stores one checkout per URL under `Root`. `Prepare` holds a per-URL lock while updating the shallow checkout, then replaces `dst` with a copy and returns its commit. The destination must be outside `Root`.

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

When a historical commit is missing from the shallow cache, `EnsureCommit` unshallows the checkout:

```go
if err := cache.EnsureCommit(ctx, url, commit); err != nil {
    log.Fatal(err)
}
```

## Read a file from a commit

`Blob` runs `git show <commit>:<path>` and reads at most `maxBytes+1`, draining the rest of stdout so Git can exit. The extra byte distinguishes content exactly at the limit from truncated content, and a NUL byte within the returned range marks the blob as binary. Check untrusted input with `ValidCommit` and `SanitizePath` before calling it:

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

## Remote queries

`RemoteBranches` returns sorted branch names from `git ls-remote --heads`. It disables terminal prompts and the ambient credential helper so a supplied URL cannot trigger credential lookup. `RemoteHead` returns the SHA advertised for `HEAD` and keeps ambient non-interactive credentials available.

```go
branches, err := clone.RemoteBranches(ctx, clone.Retry{}, url)
if err != nil {
    log.Fatal(err)
}

head, err := clone.RemoteHead(ctx, clone.Retry{}, url)
if err != nil {
    log.Fatal(err)
}
```

## Retry policy

The zero value of `Retry` allows three attempts with exponential backoff and positive jitter. `Do` retries only recognized network and remote-service failures. Permanent markers win when output contains both kinds, and an unknown message stops after the first attempt.

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
