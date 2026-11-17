package argocd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type AppSpec struct {
	Name            string
	RepoURL         string
	Chart           string
	TargetRevision  string
	ValuesYAML      string
	SetParams       []string
	ServerSideApply bool
}

var tfInterpRe = regexp.MustCompile(`\$\{[^}]+\}`)

// ParseApplicationFile parses an Argo CD `Application` YAML or Terraform `.yaml.tmpl` file,
// safely replacing `${terraform_vars}` with deterministic valid strings so Helm charts
// defined inside GitOps / Terraform templates can be pulled and verified automatically.
func ParseApplicationFile(path string) (*AppSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseApplicationBytes(raw, path)
}

func ParseApplicationBytes(raw []byte, label string) (*AppSpec, error) {
	sanitized := tfInterpRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		inner := strings.ToLower(m)
		switch {
		case strings.Contains(inner, "size"):
			return "10Gi"
		case strings.Contains(inner, "concurrent") || strings.Contains(inner, "replica") || strings.Contains(inner, "count") || strings.Contains(inner, "port"):
			return "2"
		default:
			return "stageproof-placeholder"
		}
	})

	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Source struct {
				RepoURL        string `yaml:"repoURL"`
				Chart          string `yaml:"chart"`
				TargetRevision string `yaml:"targetRevision"`
				Helm           struct {
					Values     string `yaml:"values"`
					Parameters []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"parameters"`
				} `yaml:"helm"`
			} `yaml:"source"`
			SyncPolicy struct {
				SyncOptions []string `yaml:"syncOptions"`
			} `yaml:"syncPolicy"`
		} `yaml:"spec"`
	}

	if err := yaml.Unmarshal([]byte(sanitized), &doc); err != nil {
		return nil, fmt.Errorf("parse Argo CD manifest %s: %w", label, err)
	}
	if !strings.HasPrefix(doc.APIVersion, "argoproj.io/") || doc.Kind != "Application" {
		return nil, fmt.Errorf("%s is not an argoproj.io Application (got %s %s)", label, doc.APIVersion, doc.Kind)
	}
	if doc.Spec.Source.Chart == "" || doc.Spec.Source.RepoURL == "" {
		return nil, fmt.Errorf("%s does not reference a remote Helm chart (repoURL/chart empty)", label)
	}

	out := &AppSpec{
		Name:           doc.Metadata.Name,
		RepoURL:        strings.TrimSpace(doc.Spec.Source.RepoURL),
		Chart:          strings.TrimSpace(doc.Spec.Source.Chart),
		TargetRevision: strings.TrimSpace(doc.Spec.Source.TargetRevision),
		ValuesYAML:     doc.Spec.Source.Helm.Values,
	}
	for _, p := range doc.Spec.Source.Helm.Parameters {
		if p.Name != "" {
			out.SetParams = append(out.SetParams, fmt.Sprintf("%s=%s", p.Name, p.Value))
		}
	}
	for _, opt := range doc.Spec.SyncPolicy.SyncOptions {
		if strings.EqualFold(strings.TrimSpace(opt), "ServerSideApply=true") {
			out.ServerSideApply = true
		}
	}
	return out, nil
}

// PullChart downloads and untars a specific `(repoURL, chart, version)` into destParentDir,
// returning the unpacked chart directory path (`<destParentDir>/<chart>`).
func PullChart(ctx context.Context, repoURL, chart, version, destParentDir string) (string, error) {
	if err := os.MkdirAll(destParentDir, 0o755); err != nil {
		return "", err
	}
	var args []string
	if strings.HasPrefix(repoURL, "oci://") {
		ref := strings.TrimRight(repoURL, "/") + "/" + chart
		args = []string{"pull", ref, "--version", version, "--untar", "--untardir", destParentDir}
	} else {
		args = []string{"pull", chart, "--repo", repoURL, "--version", version, "--untar", "--untardir", destParentDir}
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helm %s failed: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return filepath.Join(destParentDir, chart), nil
}
