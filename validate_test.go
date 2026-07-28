package clone

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, url := range []string{
		"https://github.com/git-pkgs/clone",
		"https://gitlab.com/example/repo.git",
	} {
		if err := ValidateURL(url); err != nil {
			t.Errorf("ValidateURL(%q): %v", url, err)
		}
	}

	for _, url := range []string{
		"http://github.com/example/repo",
		"git@github.com:example/repo.git",
		"ssh://git@example.com/repo",
		"file:///etc/passwd",
		"--upload-pack=/bin/sh",
		"",
	} {
		if err := ValidateURL(url); err == nil {
			t.Errorf("ValidateURL(%q) succeeded", url)
		}
	}
}

func TestValidateRef(t *testing.T) {
	for _, ref := range []string{
		"",
		"main",
		"release/1.0",
		"v1.2.3",
		"feature/abc_def-1",
	} {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("ValidateRef(%q): %v", ref, err)
		}
	}

	for _, ref := range []string{
		"--all",
		"foo..bar",
		"../etc/passwd",
		"branch with space",
		"branch;rm",
		"branch\nmain",
		"head@{0}",
		"refs/heads/main^",
		"branch~1",
	} {
		if err := ValidateRef(ref); err == nil {
			t.Errorf("ValidateRef(%q) succeeded", ref)
		}
	}
}

func TestValidCommit(t *testing.T) {
	cases := []struct {
		sha  string
		want bool
	}{
		{"abcd", true},
		{strings.Repeat("a", 40), true},
		{strings.Repeat("f", 64), true},
		{"abc", false},
		{strings.Repeat("a", 65), false},
		{"ABCDEF12", false},
		{"abcg", false},
		{"", false},
	}
	for _, test := range cases {
		if got := ValidCommit(test.sha); got != test.want {
			t.Errorf("ValidCommit(%q) = %v, want %v", test.sha, got, test.want)
		}
	}
}

func TestSanitizePath(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"lib/x.rb", "lib/x.rb", true},
		{"./a.go", "a.go", true},
		{"a/./b", "a/b", true},
		{"", "", false},
		{"../etc/passwd", "", false},
		{"a/../b", "", false},
		{"/etc/passwd", "", false},
		{"a\x00b", "", false},
		{"..", "", false},
		{".", "", false},
	}
	for _, test := range cases {
		got, ok := SanitizePath(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("SanitizePath(%q) = (%q, %v), want (%q, %v)",
				test.input, got, ok, test.want, test.ok)
		}
	}
}
