package helm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// userinfo matches credentials in a URL, as in https://user:secret@host.
var userinfo = regexp.MustCompile(`://[^/\s]+@`)

// Pull downloads one chart version into destParent and returns the directory
// that contains Chart.yaml. The unpacked directory name is whatever the
// archive contains, which is not always the chart name passed to helm.
// The caller creates and removes destParent.
func Pull(ctx context.Context, repoURL, chart, version, destParent string) (string, error) {
	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return "", err
	}
	args := pullArgs(repoURL, chart, version, destParent)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", pullFailed(chart, version, stderr.String())
	}
	return unpackedChart(destParent)
}

func pullArgs(repoURL, chart, version, dest string) []string {
	if strings.HasPrefix(repoURL, "oci://") {
		ref := strings.TrimRight(repoURL, "/") + "/" + chart
		return []string{"pull", ref, "--version", version, "--untar", "--untardir", dest}
	}
	args := []string{"pull", chart, "--repo", repoURL, "--version", version, "--untar", "--untardir", dest}
	if strings.HasPrefix(repoURL, "http://") {
		args = append(args, "--plain-http")
	}
	return args
}

func unpackedChart(parent string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(parent, "*", "Chart.yaml"))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("helm pull unpacked %d charts, want 1", len(matches))
	}
	return filepath.Dir(matches[0]), nil
}

func pullFailed(chart, version, stderr string) error {
	detail := scrubUserinfo(strings.TrimSpace(stderr))
	detail = strings.Join(strings.Fields(detail), " ")
	if runes := []rune(detail); len(runes) > 400 {
		detail = string(runes[:400])
	}
	if detail == "" {
		return fmt.Errorf("helm pull %s %s failed", chart, version)
	}
	return fmt.Errorf("helm pull %s %s failed: %s", chart, version, detail)
}

func scrubUserinfo(msg string) string {
	return userinfo.ReplaceAllString(msg, "://")
}
