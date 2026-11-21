package adapter

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitCommand(args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-c", "safe.directory=*")
	full = append(full, args...)
	return exec.Command("git", full...)
}

func readGitFile(ref, filePath string) ([]byte, error) {
	repoRootBytes, err := gitCommand("rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, err
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}
	relFile, err := filepath.Rel(repoRoot, absFile)
	if err != nil {
		return nil, err
	}
	out, err := gitCommand("-C", repoRoot, "show", fmt.Sprintf("%s:%s", ref, filepath.ToSlash(relFile))).Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s: %w", ref, relFile, err)
	}
	return out, nil
}
