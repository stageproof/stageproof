package argocd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

var placeholderRe = regexp.MustCompile(`\$\{[^}]+\}`)

const fixedPlaceholder = "stageproof-placeholder"

// UnresolvedError means a ${...} token is still in the chart repository, name,
// or version. The check cannot choose a chart, so the result is NOT CHECKED.
type UnresolvedError struct {
	Label  string
	Tokens []string
}

func (e *UnresolvedError) Error() string {
	return fmt.Sprintf("%s: unresolved placeholder in repoURL, chart, or targetRevision: %s", e.Label, strings.Join(e.Tokens, ", "))
}

func UnresolvedTokens(err error) []string {
	var unresolved *UnresolvedError
	if errors.As(err, &unresolved) {
		return append([]string(nil), unresolved.Tokens...)
	}
	return nil
}

// ParseApplicationFile parses an Argo CD Application YAML. ${...} tokens are
// replaced from the supplied map. A token with no entry becomes the fixed
// string stageproof-placeholder.
func ParseApplicationFile(path string, placeholders map[string]string) (*AppSpec, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseApplicationBytes(raw, path, placeholders)
}

func ParseApplicationBytes(raw []byte, label string, placeholders map[string]string) (*AppSpec, []string, error) {
	text := string(raw)
	for key, val := range placeholders {
		text = strings.ReplaceAll(text, key, val)
	}
	var used []string
	markers := map[string]string{}
	sanitized := placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		used = append(used, m)
		marker := fmt.Sprintf("stageproof-ph-%04d", len(used))
		markers[marker] = m
		return marker
	})
	sort.Strings(used)

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
		return nil, used, fmt.Errorf("parse Argo CD manifest %s: %w", label, err)
	}
	if !strings.HasPrefix(doc.APIVersion, "argoproj.io/") || doc.Kind != "Application" {
		return nil, used, fmt.Errorf("%s is not an argoproj.io Application (got %s %s)", label, doc.APIVersion, doc.Kind)
	}
	if doc.Spec.Source.Chart == "" || doc.Spec.Source.RepoURL == "" {
		return nil, used, fmt.Errorf("%s does not reference a remote Helm chart (repoURL/chart empty)", label)
	}
	restore := func(s string) (string, []string) {
		var hit []string
		for marker, token := range markers {
			if strings.Contains(s, marker) {
				hit = append(hit, token)
				s = strings.ReplaceAll(s, marker, fixedPlaceholder)
			}
		}
		sort.Strings(hit)
		return s, hit
	}
	repoURL, repoHit := restore(doc.Spec.Source.RepoURL)
	chart, chartHit := restore(doc.Spec.Source.Chart)
	revision, revHit := restore(doc.Spec.Source.TargetRevision)
	values, _ := restore(doc.Spec.Source.Helm.Values)
	required := append(append(repoHit, chartHit...), revHit...)
	sort.Strings(required)
	required = uniqueStrings(required)
	if len(required) > 0 {
		return nil, used, &UnresolvedError{Label: label, Tokens: required}
	}

	out := &AppSpec{
		Name:           doc.Metadata.Name,
		RepoURL:        strings.TrimSpace(repoURL),
		Chart:          strings.TrimSpace(chart),
		TargetRevision: strings.TrimSpace(revision),
		ValuesYAML:     values,
	}
	for _, p := range doc.Spec.Source.Helm.Parameters {
		name, _ := restore(p.Name)
		value, _ := restore(p.Value)
		if name != "" {
			out.SetParams = append(out.SetParams, fmt.Sprintf("%s=%s", name, value))
		}
	}
	for _, opt := range doc.Spec.SyncPolicy.SyncOptions {
		if strings.EqualFold(strings.TrimSpace(opt), "ServerSideApply=true") {
			out.ServerSideApply = true
		}
	}
	return out, used, nil
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := []string{in[0]}
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
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
