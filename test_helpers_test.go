package clone

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func gitTestEnv() []string {
	env := append([]string{}, os.Environ()...)
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=clone tests",
		"GIT_AUTHOR_EMAIL=clone-tests@example.invalid",
		"GIT_COMMITTER_NAME=clone tests",
		"GIT_COMMITTER_EMAIL=clone-tests@example.invalid",
	)
}

// subcommand returns the git subcommand from an argv that may be prefixed
// with `-c key=value` config overrides or `-C dir`.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-C":
			i++
		default:
			return args[i]
		}
	}
	return ""
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
