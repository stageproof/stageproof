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
	"time"
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
		if err := helmPull(cache, tgz, repo, chart, version); err != nil {
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
		matches, _ := filepath.Glob(filepath.Join(dest, "*", "Chart.yaml"))
		if len(matches) != 1 {
			return "", fmt.Errorf("unpacked chart missing Chart.yaml in %s", chartDir)
		}
		chartDir = filepath.Dir(matches[0])
	}
	return chartDir, nil
}

func helmPull(cache, dest, repo, chart, version string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		err := helmPullOnce(cache, dest, repo, chart, version)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

func helmPullOnce(cache, dest, repo, chart, version string) error {
	tmp, err := os.MkdirTemp(cache, "pull-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	args := []string{"pull", chart, "--version", version, "--destination", tmp}
	if strings.HasPrefix(repo, "oci://") {
		args = []string{"pull", strings.TrimRight(repo, "/") + "/" + chart, "--version", version, "--destination", tmp}
	} else {
		args = append(args, "--repo", repo)
	}
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm pull %s %s: %w\n%s", chart, version, err, out)
	}
	files, err := filepath.Glob(filepath.Join(tmp, "*.tgz"))
	if err != nil {
		return err
	}
	if len(files) != 1 {
		return fmt.Errorf("helm pull %s %s wrote %d archives, want 1", chart, version, len(files))
	}
	return os.Rename(files[0], dest)
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
