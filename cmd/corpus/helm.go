package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ensureChart(cache, repo, chart, version, want string) (string, error) {
	if want == "" {
		return "", fmt.Errorf("no lock entry for %s %s %s", repo, chart, version)
	}
	tgz := filepath.Join(cache, chart+"-"+version+".tgz")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	if !fileMatches(tgz, want) {
		if err := helmPull(cache, repo, chart, version); err != nil {
			return "", err
		}
		got, err := fileSHA(tgz)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", fmt.Errorf("%s sha256 %s, lock says %s", tgz, got, want)
		}
	}
	dest := filepath.Join(cache, "src", chart+"-"+version)
	marker := filepath.Join(dest, ".unpacked")
	if _, err := os.Stat(marker); err != nil {
		if err := os.RemoveAll(dest); err != nil {
			return "", err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return "", err
		}
		cmd := exec.Command("tar", "-xzf", tgz, "-C", dest)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("untar %s: %w\n%s", tgz, err, out)
		}
		if err := os.WriteFile(marker, []byte("ok\n"), 0o644); err != nil {
			return "", err
		}
	}
	chartDir := filepath.Join(dest, chart)
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err != nil {
		return "", fmt.Errorf("unpacked chart missing Chart.yaml in %s", chartDir)
	}
	return chartDir, nil
}

func helmPull(dir, repo, chart, version string) error {
	args := []string{"pull", chart, "--version", version, "--destination", dir}
	if strings.HasPrefix(repo, "oci://") {
		args = []string{"pull", strings.TrimRight(repo, "/") + "/" + chart, "--version", version, "--destination", dir}
	} else {
		args = append(args, "--repo", repo)
	}
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm pull %s %s: %w\n%s", chart, version, err, out)
	}
	return nil
}

func fileMatches(path, sha string) bool {
	got, err := fileSHA(path)
	return err == nil && got == sha
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
