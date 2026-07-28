package clone

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

var commitRE = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// ValidateURL rejects Git URLs that do not use HTTPS.
func ValidateURL(url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("only https:// URLs are allowed, got %q", url)
	}
	return nil
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
