package clone

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

var commitRE = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// ValidateURL rejects Git URLs that do not use HTTPS, do not parse, or contain
// control bytes. Embedded userinfo is accepted (some callers use
// https://<token>@host/... for private repos) but should be redacted before
// logging; UnreachableError.Error and this package's own error strings do so
// via RedactURL.
func ValidateURL(raw string) error {
	for i := range len(raw) {
		if c := raw[i]; c < 0x20 || c == 0x7f {
			return fmt.Errorf("URL contains control byte 0x%02x at offset %d", c, i)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", RedactURL(raw), err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("only https:// URLs are allowed, got %q", RedactURL(raw))
	}
	if u.Host == "" {
		return fmt.Errorf("URL %q has no host", RedactURL(raw))
	}
	return nil
}

// RedactURL replaces any userinfo in raw with a fixed placeholder so error
// messages and logs cannot leak an embedded token. A URL that fails to parse
// is returned unchanged: url.Parse does not accept control bytes, so an
// unparseable string here is one ValidateURL would already have rejected for a
// reason unrelated to its credential.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("REDACTED")
	return u.String()
}

// ValidateRef restricts refs to a conservative branch and tag name character
// set before they are passed to Git.
func ValidateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid ref %q: must not start with -", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf(`invalid ref %q: must not contain ".."`, ref)
	}
	for _, char := range ref {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '.', char == '_', char == '/', char == '-':
		default:
			return fmt.Errorf("invalid ref %q: contains disallowed character %q", ref, char)
		}
	}
	return nil
}

// ValidCommit reports whether sha is a lowercase hexadecimal object ID or
// abbreviated object ID between 4 and 64 characters long.
func ValidCommit(sha string) bool {
	return commitRE.MatchString(sha)
}

// SanitizePath returns a slash-form path safe to use in a Git object
// expression. It rejects empty and absolute paths, NUL bytes, and traversal.
func SanitizePath(value string) (string, bool) {
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") {
		return "", false
	}
	value = strings.TrimPrefix(value, "./")
	if slices.Contains(strings.Split(value, "/"), "..") {
		return "", false
	}
	clean := path.Clean(value)
	if clean == "." || clean == "" {
		return "", false
	}
	return clean, true
}
