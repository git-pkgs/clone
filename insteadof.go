package clone

import (
	"context"
	"strings"
)

// doPinnedURL runs cmd, and retries it once with url pinned to itself when Git
// refused the transport the command ended up on.
//
// Git applies url.<base>.insteadOf rewriting after ValidateURL has already
// approved an https:// input, so a global ~/.gitconfig such as
//
//	[url "ssh://git@github.com/"]
//		insteadOf = https://github.com/
//
// silently changes the transport of every github.com URL a caller passes in.
// remoteEnv's GIT_ALLOW_PROTOCOL whitelist then refuses the rewritten
// transport, and the caller is left with "fatal: transport 'ssh' not allowed"
// for a URL it never asked to have rewritten. The whitelist is doing its job --
// the rewrite really did move the request off the URL that was validated -- but
// failure is the only outcome it can offer, on a machine whose Git config is
// otherwise perfectly ordinary.
//
// Git resolves insteadOf by longest match, so mapping the whole URL to itself
// outranks any prefix rule and restores the validated URL. That is deliberately
// narrower than GIT_CONFIG_GLOBAL=os.DevNull, which would also discard the
// proxy, CA-bundle and credential configuration a user legitimately keeps in
// the same file.
//
// The pin is applied only after Git has already refused, which is what keeps it
// from changing any working setup:
//
//   - A rewrite between https:// URLs, the usual internal-mirror case, is never
//     refused, so it is never touched.
//   - A rewrite onto a transport the caller allowed through GIT_ALLOW_PROTOCOL
//     is not refused either, so a deliberate opt-in still wins.
//
// Letting Git decide also avoids second-guessing its rewrite rules here: the
// scp-like shorthand, longest-match ordering and protocol naming stay Git's to
// interpret. The retry costs one extra invocation, and only on a command that
// has already failed: a refusal matches no transient marker, so TransientFailure
// treats it as permanent and the first Do returns after a single attempt with no
// backoff.
func doPinnedURL(ctx context.Context, retry Retry, url string, cmd Command) (string, error) {
	out, err := retry.Do(ctx, cmd)
	if err == nil || url == "" || !transportRefused(out) {
		return out, err
	}
	pinned := cmd
	pinned.Args = append(pinArgs(url), cmd.Args...)
	return retry.Do(ctx, pinned)
}

// pinArgs returns the Git configuration that maps url to itself, so Git's
// longest-match rule prefers it over any ambient prefix rewrite.
func pinArgs(url string) []string {
	return []string{"-c", "url." + url + ".insteadOf=" + url}
}

// transportRefused reports whether out is Git refusing a transport that the
// GIT_ALLOW_PROTOCOL whitelist does not list, as opposed to any other failure.
func transportRefused(out string) bool {
	_, rest, ok := strings.Cut(out, "transport '")
	return ok && strings.Contains(rest, "' not allowed")
}
