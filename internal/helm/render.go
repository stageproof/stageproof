package helm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type RenderOptions struct {
	ReleaseName string
	Namespace   string
	ChartDir    string
	ValuesFiles []string
	SetValues   []string
	SetStrings  []string
	KubeVersion string
	APIVersions []string
}

func RenderChart(ctx context.Context, opts RenderOptions) (string, error) {
	rel := opts.ReleaseName
	if rel == "" {
		rel = "stageproof-rel"
	}
	ns := opts.Namespace
	if ns == "" {
		ns = "stageproof-check"
	}

	args := []string{
		"template", rel, opts.ChartDir,
		"--namespace", ns,
		"--include-crds",
	}
	if opts.KubeVersion != "" {
		kv := strings.TrimPrefix(opts.KubeVersion, "v")
		if strings.Count(kv, ".") == 1 {
			kv += ".0"
		}
		args = append(args, "--kube-version", kv)
	}
	for _, av := range opts.APIVersions {
		args = append(args, "--api-versions", av)
	}
	for _, vf := range opts.ValuesFiles {
		args = append(args, "-f", vf)
	}
	for _, sv := range opts.SetValues {
		args = append(args, "--set", sv)
	}
	for _, ss := range opts.SetStrings {
		args = append(args, "--set-string", ss)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helm template %s failed: %w\n%s", opts.ChartDir, err, strings.TrimSpace(stderr.String()))
	}

	crdsDir := filepath.Join(opts.ChartDir, "crds")
	var crdBuf strings.Builder
	if entries, err := os.ReadDir(crdsDir); err == nil {
		outStr := stdout.String()
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".yaml" && ext != ".yml" {
				continue
			}
			raw, rErr := os.ReadFile(filepath.Join(crdsDir, e.Name()))
			if rErr == nil && len(raw) > 0 && !strings.Contains(outStr, string(raw[:min(len(raw), 120)])) {
				crdBuf.WriteString("\n---\n")
				crdBuf.Write(raw)
			}
		}
	}

	return stdout.String() + crdBuf.String(), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
