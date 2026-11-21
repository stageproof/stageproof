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
	// SkipCRDs omits CRDs from the rendered output. Argo CD sets this from
	// spec.source.helm.skipCrds, which is Helm's --skip-crds.
	// https://argo-cd.readthedocs.io/en/stable/user-guide/helm/
	SkipCRDs bool
	SetFiles []string
}

func RenderChart(ctx context.Context, opts RenderOptions) (string, error) {
	return RenderChartBinary(ctx, "helm", opts)
}

// RenderChartBinary renders with an explicit helm executable.
// The argument is a path or a name on PATH. An empty name means helm.
func RenderChartBinary(ctx context.Context, bin string, opts RenderOptions) (string, error) {
	if bin == "" {
		bin = "helm"
	}
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
	}
	if opts.SkipCRDs {
		args = append(args, "--skip-crds")
	} else {
		args = append(args, "--include-crds")
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
	for _, sf := range opts.SetFiles {
		args = append(args, "--set-file", sf)
	}
	return renderChart(ctx, bin, args, opts)
}

func renderChart(ctx context.Context, bin string, args []string, opts RenderOptions) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s template %s failed: %w\n%s", bin, opts.ChartDir, err, strings.TrimSpace(stderr.String()))
	}

	var crdBuf strings.Builder
	if !opts.SkipCRDs {
		crdBuf = appendedChartCRDs(opts.ChartDir, stdout.String())
	}

	return stdout.String() + crdBuf.String(), nil
}

func appendedChartCRDs(chartDir, rendered string) strings.Builder {
	var crdBuf strings.Builder
	crdsDir := filepath.Join(chartDir, "crds")
	entries, err := os.ReadDir(crdsDir)
	if err != nil {
		return crdBuf
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		raw, rErr := os.ReadFile(filepath.Join(crdsDir, e.Name()))
		if rErr == nil && len(raw) > 0 && !strings.Contains(rendered, string(raw[:min(len(raw), 120)])) {
			crdBuf.WriteString("\n---\n")
			crdBuf.Write(raw)
		}
	}
	return crdBuf
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
