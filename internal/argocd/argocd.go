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

// AppSpec is the Helm chart an Argo CD Application asked to render.
//
// Precedence and the fields below were checked against the stable docs and
// the Argo CD source on 2026-11-21, not from memory:
//
//   - https://argo-cd.readthedocs.io/en/stable/user-guide/helm/
//     "Order of precedence is parameters > valuesObject > values > valueFiles > helm repository values.yaml."
//     "valuesObject trumps values - therefore values will be ignored."
//     "When multiple valueFiles are specified, the last file listed has the highest precedence."
//     helm.releaseName defaults to the Application name.
//     helm.skipCrds is Helm's --skip-crds.
//   - https://argo-cd.readthedocs.io/en/stable/user-guide/application-specification/
//     valueFiles are relative to the source path. ignoreMissingValueFiles defaults to false.
//   - https://argo-cd.readthedocs.io/en/stable/user-guide/multiple_sources/
//     $ref is only valid at the start of a value file path. The rest is relative
//     to the root of that source, even when the source also has a path.
//     A source with ref cannot set chart. If that source sets path, Argo CD
//     also generates manifests from it.
//   - https://github.com/argoproj/argo-cd/blob/master/pkg/apis/application/v1alpha1/types.go
//     GetSources uses spec.sources when it is non-empty and ignores spec.source.
//   - https://github.com/argoproj/argo-cd/blob/master/pkg/apis/application/v1alpha1/values.go
//     ValuesYAML returns valuesObject when it is set, and does not merge values under it.
type AppSpec struct {
	Name            string
	RepoURL         string
	Chart           string
	TargetRevision  string
	LocalPath       string
	ValueFiles      []string
	ValuesYAML      string
	ValuesKind      string // "values", "valuesObject", or empty
	SetParams       []string
	SetStrings      []string
	FileParams      []FileParam
	ReleaseName     string
	Namespace       string
	SkipCrds        *bool
	IgnoreMissing   bool
	ServerSideApply bool
	Refs            map[string]string
	Label           string
}

// FileParam is one helm.fileParameters entry. Path is still unresolved.
type FileParam struct {
	Name string
	Path string
}

var placeholderRe = regexp.MustCompile(`\$\{[^}]+\}`)

const fixedPlaceholder = "stageproof-placeholder"

// UnresolvedError means a ${...} token is still in the chart repository, name,
// path, or version. The check cannot choose a chart, so the result is NOT CHECKED.
type UnresolvedError struct {
	Label  string
	Tokens []string
}

func (e *UnresolvedError) Error() string {
	return fmt.Sprintf("%s: unresolved placeholder in repoURL, path, chart, or targetRevision: %s", e.Label, strings.Join(e.Tokens, ", "))
}

// NotCheckedError is a chart this command will not render. The reason is safe
// to show. Callers must not fall back to the chart's default values.
type NotCheckedError struct {
	Reason string
}

func (e *NotCheckedError) Error() string { return e.Reason }

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

type helmDoc struct {
	ValueFiles []string `yaml:"valueFiles"`
	Parameters []struct {
		Name        string `yaml:"name"`
		Value       string `yaml:"value"`
		ForceString bool   `yaml:"forceString"`
	} `yaml:"parameters"`
	FileParameters []struct {
		Name string `yaml:"name"`
		Path string `yaml:"path"`
	} `yaml:"fileParameters"`
	ReleaseName             string    `yaml:"releaseName"`
	Values                  string    `yaml:"values"`
	ValuesObject            yaml.Node `yaml:"valuesObject"`
	IgnoreMissingValueFiles bool      `yaml:"ignoreMissingValueFiles"`
	SkipCrds                *bool     `yaml:"skipCrds"`
	Namespace               string    `yaml:"namespace"`
}

type sourceDoc struct {
	RepoURL        string   `yaml:"repoURL"`
	Path           string   `yaml:"path"`
	Chart          string   `yaml:"chart"`
	TargetRevision string   `yaml:"targetRevision"`
	Ref            string   `yaml:"ref"`
	Helm           *helmDoc `yaml:"helm"`
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
			Source         *sourceDoc  `yaml:"source"`
			Sources        []sourceDoc `yaml:"sources"`
			SourceHydrator *struct{}   `yaml:"sourceHydrator"`
			Destination    struct {
				Namespace string `yaml:"namespace"`
			} `yaml:"destination"`
			SyncPolicy struct {
				SyncOptions []string `yaml:"syncOptions"`
			} `yaml:"syncPolicy"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(sanitized), &doc); err != nil {
		return nil, used, fmt.Errorf("parse Argo CD manifest %s: %w", label, err)
	}
	if strings.HasPrefix(doc.APIVersion, "argoproj.io/") && doc.Kind == "ApplicationSet" {
		return nil, used, &NotCheckedError{Reason: label + " is an ApplicationSet. Stageproof does not expand generators."}
	}
	if !strings.HasPrefix(doc.APIVersion, "argoproj.io/") || doc.Kind != "Application" {
		return nil, used, fmt.Errorf("%s is not an argoproj.io Application (got %s %s)", label, doc.APIVersion, doc.Kind)
	}
	if doc.Spec.SourceHydrator != nil {
		return nil, used, &NotCheckedError{Reason: label + " sets sourceHydrator. Stageproof does not render a hydrated source."}
	}

	// GetSources uses spec.sources whenever that list is non-empty.
	sources := doc.Spec.Sources
	if len(sources) == 0 && doc.Spec.Source != nil {
		sources = []sourceDoc{*doc.Spec.Source}
	}
	chartSrc, refs, selErr := selectChartSource(label, sources)
	if selErr != nil {
		return nil, used, selErr
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
	repoURL, repoHit := restore(chartSrc.RepoURL)
	chartName, chartHit := restore(chartSrc.Chart)
	path, pathHit := restore(chartSrc.Path)
	revision, revHit := restore(chartSrc.TargetRevision)
	required := append(append(append(repoHit, chartHit...), pathHit...), revHit...)
	restoredRefs := make(map[string]string, len(refs))
	for name, refURL := range refs {
		got, hit := restore(refURL)
		restoredRefs[name] = strings.TrimSpace(got)
		required = append(required, hit...)
	}
	sort.Strings(required)
	required = uniqueStrings(required)
	if len(required) > 0 {
		return nil, used, &UnresolvedError{Label: label, Tokens: required}
	}

	valuesYAML, valuesKind, vErr := helmValues(chartSrc.Helm, restore)
	if vErr != nil {
		return nil, used, fmt.Errorf("parse helm values in %s: %w", label, vErr)
	}
	out := &AppSpec{
		Name:           strings.TrimSpace(doc.Metadata.Name),
		RepoURL:        strings.TrimSpace(repoURL),
		Chart:          strings.TrimSpace(chartName),
		TargetRevision: strings.TrimSpace(revision),
		LocalPath:      strings.TrimSpace(path),
		ValuesYAML:     valuesYAML,
		ValuesKind:     valuesKind,
		ReleaseName:    strings.TrimSpace(doc.Metadata.Name),
		Namespace:      strings.TrimSpace(doc.Spec.Destination.Namespace),
		Refs:           restoredRefs,
		Label:          label,
	}
	if chartSrc.Helm != nil {
		for _, file := range chartSrc.Helm.ValueFiles {
			got, _ := restore(file)
			got = strings.TrimSpace(got)
			if got != "" {
				out.ValueFiles = append(out.ValueFiles, got)
			}
		}
		for _, p := range chartSrc.Helm.Parameters {
			name, _ := restore(p.Name)
			value, _ := restore(p.Value)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			pair := name + "=" + value
			if p.ForceString {
				out.SetStrings = append(out.SetStrings, pair)
			} else {
				out.SetParams = append(out.SetParams, pair)
			}
		}
		for _, p := range chartSrc.Helm.FileParameters {
			name, _ := restore(p.Name)
			path, _ := restore(p.Path)
			name = strings.TrimSpace(name)
			path = strings.TrimSpace(path)
			if name == "" || path == "" {
				continue
			}
			out.FileParams = append(out.FileParams, FileParam{Name: name, Path: path})
		}
		if rel := strings.TrimSpace(chartSrc.Helm.ReleaseName); rel != "" {
			got, _ := restore(rel)
			out.ReleaseName = strings.TrimSpace(got)
		}
		if ns := strings.TrimSpace(chartSrc.Helm.Namespace); ns != "" {
			got, _ := restore(ns)
			out.Namespace = strings.TrimSpace(got)
		}
		out.SkipCrds = chartSrc.Helm.SkipCrds
		out.IgnoreMissing = chartSrc.Helm.IgnoreMissingValueFiles
	}
	if out.Chart != "" && out.RepoURL == "" {
		return nil, used, fmt.Errorf("%s does not reference a remote Helm chart (repoURL/chart empty)", label)
	}
	if out.LocalPath != "" && out.RepoURL == "" {
		return nil, used, &NotCheckedError{Reason: label + " sets a Git path but repoURL is empty, so Stageproof cannot tell whether the chart is in this checkout."}
	}
	for _, opt := range doc.Spec.SyncPolicy.SyncOptions {
		if strings.EqualFold(strings.TrimSpace(opt), "ServerSideApply=true") {
			out.ServerSideApply = true
		}
	}
	return out, used, nil
}

// selectChartSource picks the one Helm chart and the ref sources.
// spec.sources, when non-empty, already replaced spec.source.
func selectChartSource(label string, sources []sourceDoc) (sourceDoc, map[string]string, error) {
	refs := map[string]string{}
	var charts []sourceDoc
	for _, src := range sources {
		if strings.TrimSpace(src.Ref) != "" {
			name := strings.TrimSpace(src.Ref)
			if strings.TrimSpace(src.Chart) != "" {
				return sourceDoc{}, nil, &NotCheckedError{Reason: fmt.Sprintf("%s source ref %s also sets chart. Argo CD does not use another chart as a values source.", label, name)}
			}
			if strings.TrimSpace(src.Path) != "" {
				return sourceDoc{}, nil, &NotCheckedError{Reason: fmt.Sprintf("%s source ref %s also sets path. Argo CD would generate manifests from that directory as well as reading values, and this check does not combine those manifests.", label, name)}
			}
			if _, ok := refs[name]; ok {
				return sourceDoc{}, nil, &NotCheckedError{Reason: fmt.Sprintf("%s declares source ref %s more than once", label, name)}
			}
			refs[name] = src.RepoURL
			continue
		}
		if strings.TrimSpace(src.Chart) != "" && strings.TrimSpace(src.Path) != "" {
			return sourceDoc{}, nil, &NotCheckedError{Reason: label + " sets both path and chart on one source. Argo CD uses chart for a Helm repository and path for a Git directory."}
		}
		if strings.TrimSpace(src.Chart) != "" || strings.TrimSpace(src.Path) != "" {
			charts = append(charts, src)
		}
	}
	if len(charts) == 0 {
		return sourceDoc{}, nil, fmt.Errorf("%s does not reference a Helm chart (repoURL/chart and path are empty)", label)
	}
	if len(charts) > 1 {
		return sourceDoc{}, nil, &NotCheckedError{Reason: fmt.Sprintf("%s has %d chart sources. This check reads one Helm chart.", label, len(charts))}
	}
	return charts[0], refs, nil
}

// helmValues applies the documented valuesObject rule: a set valuesObject
// replaces values. The two are not merged.
func helmValues(helm *helmDoc, restore func(string) (string, []string)) (string, string, error) {
	if helm == nil {
		return "", "", nil
	}
	if helm.ValuesObject.Kind != 0 {
		var body any
		if err := helm.ValuesObject.Decode(&body); err != nil {
			return "", "", err
		}
		raw, err := yaml.Marshal(body)
		if err != nil {
			return "", "", err
		}
		text, _ := restore(string(raw))
		return text, "valuesObject", nil
	}
	text, _ := restore(helm.Values)
	if strings.TrimSpace(text) == "" {
		return "", "", nil
	}
	return text, "values", nil
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

// PullChart downloads and untars a specific (repoURL, chart, version) into destParentDir,
// returning the unpacked chart directory path (<destParentDir>/<chart>).
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
