// Package flux reads a Flux HelmRelease and the source objects it names.
//
// The fields below were checked against the Flux docs and the helm-controller
// types on 2026-11-21, not from memory:
//
//   - https://fluxcd.io/flux/components/helm/helmreleases/
//     One of spec.chart or spec.chartRef is set, not both.
//     spec.chart.kind defaults to HelmChart. HelmChart requires spec.chart and
//     spec.chart.spec.sourceRef. OCIRepository requires spec.chart.spec.url.
//     spec.chartRef kind is OCIRepository or HelmChart.
//     spec.valuesFrom entries merge in order. Later entries overwrite earlier
//     ones, then spec.values overwrites those. An entry with targetPath
//     overwrites everything before it, including spec.values.
//     valuesKey defaults to values.yaml. optional defaults to false.
//     spec.releaseName defaults to the name, or targetNamespace-name when
//     targetNamespace is set. A result longer than 53 characters is the first
//     40 characters, a dash, and the first 12 hex characters of the SHA-256
//     of the full name.
//   - https://github.com/fluxcd/helm-controller/blob/main/api/v2/helmrelease_types.go
//     GetReleaseName implements that composition.
//     spec.install.crds defaults to Create.
//     spec.upgrade.crds defaults to Skip: CRDs are neither installed nor upgraded.
//     The user guide lists Create as the default for both fields. The Upgrade
//     field comment says the upgrade default is Skip, and an upgrade check
//     uses that comment.
//   - https://fluxcd.io/flux/components/source/helmcharts/
//     spec.version applies to a HelmRepository source and is ignored for a
//     GitRepository. It defaults to *.
//     spec.valuesFiles paths are relative to the source. They are merged in
//     order, last wins, and values.yaml is not included unless it is listed.
//     ignoreMissingValuesFiles defaults to false.
//   - https://fluxcd.io/flux/components/source/gitrepositories/
//     spec.url is the Git remote. A remote that is not this checkout is not cloned.
//   - https://fluxcd.io/flux/components/source/ocirepositories/
//     spec.url is the OCI artifact. spec.ref is a tag, a semver range, or a digest.
//     spec.insecure allows an HTTP registry.
package flux

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const fixedPlaceholder = "stageproof-placeholder"

var placeholderRe = regexp.MustCompile(`\$\{[^}]+\}`)

// Release is one HelmRelease after placeholders in the chart identity are rejected.
type Release struct {
	Name            string
	Namespace       string
	TargetNamespace string
	ReleaseName     string
	// UpgradeCRDs is spec.upgrade.crds. Empty means the field was absent.
	// The upgrade default is Skip, not the install default Create.
	UpgradeCRDs string
	Chart       ChartRef
	Inline      map[string]any
	ValuesFrom  []ValueFrom
	// Used lists ${...} tokens that were replaced outside the chart identity.
	Used  []string
	Label string
}

// ChartRef is spec.chart or spec.chartRef, before source objects are loaded.
type ChartRef struct {
	// Mode is "template" or "ref".
	Mode          string
	TemplateKind  string
	Chart         string
	Version       string
	SourceRef     ObjectRef
	OCI           OCIRef
	ValuesFiles   []string
	IgnoreMissing bool
	Ref           ObjectRef
}

// ObjectRef is a named Flux or Kubernetes object.
type ObjectRef struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
}

// OCIRef is an inline OCIRepository chart template.
type OCIRef struct {
	URL      string
	Tag      string
	Semver   string
	Digest   string
	Insecure bool
}

// ValueFrom is one spec.valuesFrom entry.
type ValueFrom struct {
	Kind       string
	Name       string
	ValuesKey  string
	TargetPath string
	Optional   bool
	Literal    bool
}

// NotCheckedError means the HelmRelease cannot be rendered. The caller must
// not clone a repository and must not render chart defaults in its place.
type NotCheckedError struct {
	Reason string
}

func (e *NotCheckedError) Error() string {
	if e == nil {
		return "flux source was not checked"
	}
	return e.Reason
}

// UnresolvedError means a ${...} token is still in the chart name, version,
// repository URL, source name, or values file path.
type UnresolvedError struct {
	Label  string
	Tokens []string
}

func (e *UnresolvedError) Error() string {
	return fmt.Sprintf("%s: unresolved placeholder in chart, version, repository, or values file path: %s", e.Label, strings.Join(e.Tokens, ", "))
}

// UnresolvedTokens returns the tokens when err is an UnresolvedError.
func UnresolvedTokens(err error) []string {
	var unresolved *UnresolvedError
	if errors.As(err, &unresolved) {
		return append([]string(nil), unresolved.Tokens...)
	}
	return nil
}

// ReleaseName is the Helm release name Flux passes to Helm.
// https://fluxcd.io/flux/components/helm/helmreleases/#release-name
// https://github.com/fluxcd/helm-controller/blob/main/api/v2/helmrelease_types.go GetReleaseName
func ReleaseName(targetNamespace, name, explicit string) string {
	n := strings.TrimSpace(explicit)
	if n == "" {
		if strings.TrimSpace(targetNamespace) != "" {
			n = strings.TrimSpace(targetNamespace) + "-" + strings.TrimSpace(name)
		} else {
			n = strings.TrimSpace(name)
		}
	}
	if len(n) <= 53 {
		return n
	}
	sum := sha256.Sum256([]byte(n))
	return n[:40] + "-" + hex.EncodeToString(sum[:])[:12]
}

// ParseFile reads one YAML file. A file that is not a HelmRelease returns an
// error that is not a NotCheckedError, so discovery can ignore it.
func ParseFile(path string, placeholders map[string]string) (*Release, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseBytes(raw, path, placeholders)
}

// ParseBytes reads the HelmRelease document in raw.
func ParseBytes(raw []byte, label string, placeholders map[string]string) (*Release, []string, error) {
	text, markers := substitute(string(raw), placeholders)
	dec := yaml.NewDecoder(strings.NewReader(text))
	var found *Release
	var used []string
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("parse Flux manifest %s: %w", label, err)
		}
		rel, docUsed, err := releaseFromNode(&node, label, markers)
		if errors.Is(err, errNotRelease) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		found = rel
		used = append(used, docUsed...)
	}
	if found == nil {
		return nil, nil, fmt.Errorf("%s is not a helm.toolkit.fluxcd.io HelmRelease", label)
	}
	sort.Strings(used)
	found.Used = unique(used)
	found.Label = label
	return found, found.Used, nil
}

var errNotRelease = errors.New("not a HelmRelease")

func releaseFromNode(node *yaml.Node, label string, markers map[string]string) (*Release, []string, error) {
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			Chart *struct {
				Kind string    `yaml:"kind"`
				Spec yaml.Node `yaml:"spec"`
			} `yaml:"chart"`
			ChartRef *struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Name       string `yaml:"name"`
				Namespace  string `yaml:"namespace"`
			} `yaml:"chartRef"`
			ReleaseName     string    `yaml:"releaseName"`
			TargetNamespace string    `yaml:"targetNamespace"`
			Values          yaml.Node `yaml:"values"`
			ValuesFrom      []struct {
				Kind       string `yaml:"kind"`
				Name       string `yaml:"name"`
				ValuesKey  string `yaml:"valuesKey"`
				TargetPath string `yaml:"targetPath"`
				Optional   bool   `yaml:"optional"`
				Literal    bool   `yaml:"literal"`
			} `yaml:"valuesFrom"`
			Upgrade *struct {
				CRDs *string `yaml:"crds"`
			} `yaml:"upgrade"`
		} `yaml:"spec"`
	}
	if err := node.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("parse Flux manifest %s: %w", label, err)
	}
	if doc.Kind != "HelmRelease" || !strings.HasPrefix(doc.APIVersion, "helm.toolkit.fluxcd.io/") {
		return nil, nil, errNotRelease
	}
	switch doc.APIVersion {
	case "helm.toolkit.fluxcd.io/v2", "helm.toolkit.fluxcd.io/v2beta1", "helm.toolkit.fluxcd.io/v2beta2":
	default:
		return nil, nil, &NotCheckedError{Reason: label + " apiVersion " + doc.APIVersion + " is not a supported HelmRelease. Supported versions are v2, v2beta1, and v2beta2."}
	}

	chartSet := doc.Spec.Chart != nil && (doc.Spec.Chart.Kind != "" || doc.Spec.Chart.Spec.Kind != 0)
	refSet := doc.Spec.ChartRef != nil && (doc.Spec.ChartRef.Kind != "" || doc.Spec.ChartRef.Name != "")
	if chartSet && refSet {
		return nil, nil, &NotCheckedError{Reason: label + " sets both spec.chart and spec.chartRef. Flux requires one of them."}
	}
	if !chartSet && !refSet {
		return nil, nil, &NotCheckedError{Reason: label + " sets neither spec.chart nor spec.chartRef."}
	}

	var identity []string
	var used []string
	take := func(s string, chartIdentity bool) string {
		got, hit := restore(s, markers)
		if chartIdentity {
			identity = append(identity, hit...)
		} else {
			used = append(used, hit...)
		}
		return strings.TrimSpace(got)
	}

	rel := &Release{
		Name:            take(doc.Metadata.Name, false),
		Namespace:       take(doc.Metadata.Namespace, false),
		TargetNamespace: take(doc.Spec.TargetNamespace, false),
		ReleaseName:     take(doc.Spec.ReleaseName, false),
	}
	if doc.Spec.Upgrade != nil && doc.Spec.Upgrade.CRDs != nil {
		policy := take(*doc.Spec.Upgrade.CRDs, false)
		switch policy {
		case "Skip", "Create", "CreateReplace":
			rel.UpgradeCRDs = policy
		default:
			if err := identityErr(label, identity); err != nil {
				return nil, nil, err
			}
			return nil, nil, &NotCheckedError{Reason: label + " spec.upgrade.crds is " + policy + ". The supported values are Skip, Create, and CreateReplace."}
		}
	}
	if doc.Spec.Values.Kind != 0 {
		var inline map[string]any
		if err := doc.Spec.Values.Decode(&inline); err != nil {
			return nil, nil, fmt.Errorf("parse spec.values in %s: %w", label, err)
		}
		rel.Inline = inline
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(&doc.Spec.Values); err != nil {
			_ = enc.Close()
			return nil, nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, nil, err
		}
		_, hit := restore(buf.String(), markers)
		used = append(used, hit...)
		rel.Inline, _ = restoreInline(rel.Inline, markers).(map[string]any)
	}
	for _, v := range doc.Spec.ValuesFrom {
		kind := take(v.Kind, false)
		if kind != "ConfigMap" && kind != "Secret" {
			if err := identityErr(label, identity); err != nil {
				return nil, nil, err
			}
			return nil, nil, &NotCheckedError{Reason: label + " valuesFrom kind " + kind + " is not ConfigMap or Secret."}
		}
		rel.ValuesFrom = append(rel.ValuesFrom, ValueFrom{
			Kind:       kind,
			Name:       take(v.Name, false),
			ValuesKey:  take(v.ValuesKey, false),
			TargetPath: take(v.TargetPath, false),
			Optional:   v.Optional,
			Literal:    v.Literal,
		})
	}

	if refSet {
		rel.Chart.Mode = "ref"
		rel.Chart.Ref = ObjectRef{
			APIVersion: take(doc.Spec.ChartRef.APIVersion, true),
			Kind:       take(doc.Spec.ChartRef.Kind, true),
			Name:       take(doc.Spec.ChartRef.Name, true),
			Namespace:  take(doc.Spec.ChartRef.Namespace, false),
		}
		switch rel.Chart.Ref.Kind {
		case "OCIRepository", "HelmChart":
		case "ExternalArtifact":
			if err := identityErr(label, identity); err != nil {
				return nil, nil, err
			}
			return nil, nil, &NotCheckedError{Reason: label + " chartRef kind ExternalArtifact is not read."}
		default:
			if err := identityErr(label, identity); err != nil {
				return nil, nil, err
			}
			return nil, nil, &NotCheckedError{Reason: label + " chartRef kind " + rel.Chart.Ref.Kind + " is not read. Supported kinds are OCIRepository and HelmChart."}
		}
	} else {
		rel.Chart.Mode = "template"
		rel.Chart.TemplateKind = take(doc.Spec.Chart.Kind, true)
		if rel.Chart.TemplateKind == "" {
			rel.Chart.TemplateKind = "HelmChart"
		}
		var spec struct {
			Chart     string `yaml:"chart"`
			Version   string `yaml:"version"`
			SourceRef struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Name       string `yaml:"name"`
				Namespace  string `yaml:"namespace"`
			} `yaml:"sourceRef"`
			ValuesFiles              []string `yaml:"valuesFiles"`
			IgnoreMissingValuesFiles bool     `yaml:"ignoreMissingValuesFiles"`
			URL                      string   `yaml:"url"`
			Insecure                 bool     `yaml:"insecure"`
			Ref                      struct {
				Tag    string `yaml:"tag"`
				Semver string `yaml:"semver"`
				Digest string `yaml:"digest"`
			} `yaml:"ref"`
		}
		if doc.Spec.Chart.Spec.Kind != 0 {
			if err := doc.Spec.Chart.Spec.Decode(&spec); err != nil {
				return nil, nil, fmt.Errorf("parse spec.chart.spec in %s: %w", label, err)
			}
		}
		switch rel.Chart.TemplateKind {
		case "HelmChart":
			rel.Chart.Chart = take(spec.Chart, true)
			rel.Chart.Version = take(spec.Version, true)
			rel.Chart.SourceRef = ObjectRef{
				APIVersion: take(spec.SourceRef.APIVersion, true),
				Kind:       take(spec.SourceRef.Kind, true),
				Name:       take(spec.SourceRef.Name, true),
				Namespace:  take(spec.SourceRef.Namespace, false),
			}
			rel.Chart.IgnoreMissing = spec.IgnoreMissingValuesFiles
			for _, file := range spec.ValuesFiles {
				rel.Chart.ValuesFiles = append(rel.Chart.ValuesFiles, take(file, true))
			}
			if rel.Chart.Chart == "" || rel.Chart.SourceRef.Name == "" || rel.Chart.SourceRef.Kind == "" {
				if err := identityErr(label, identity); err != nil {
					return nil, nil, err
				}
				return nil, nil, &NotCheckedError{Reason: label + " spec.chart.spec.chart and spec.chart.spec.sourceRef are required when spec.chart.kind is HelmChart."}
			}
		case "OCIRepository":
			rel.Chart.OCI = OCIRef{
				URL:      take(spec.URL, true),
				Tag:      take(spec.Ref.Tag, true),
				Semver:   take(spec.Ref.Semver, true),
				Digest:   take(spec.Ref.Digest, true),
				Insecure: spec.Insecure,
			}
			if rel.Chart.OCI.URL == "" {
				if err := identityErr(label, identity); err != nil {
					return nil, nil, err
				}
				return nil, nil, &NotCheckedError{Reason: label + " spec.chart.spec.url is required when spec.chart.kind is OCIRepository."}
			}
		default:
			if err := identityErr(label, identity); err != nil {
				return nil, nil, err
			}
			return nil, nil, &NotCheckedError{Reason: label + " spec.chart.kind " + rel.Chart.TemplateKind + " is not read. Supported kinds are HelmChart and OCIRepository."}
		}
	}
	identity = unique(identity)
	if len(identity) > 0 {
		return nil, nil, &UnresolvedError{Label: label, Tokens: identity}
	}
	return rel, unique(used), nil
}

func restoreInline(v any, markers map[string]string) any {
	switch t := v.(type) {
	case string:
		got, _ := restore(t, markers)
		return got
	case map[string]any:
		for k, child := range t {
			t[k] = restoreInline(child, markers)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = restoreInline(child, markers)
		}
		return t
	default:
		return v
	}
}

func substitute(text string, known map[string]string) (string, map[string]string) {
	for key, val := range known {
		text = strings.ReplaceAll(text, key, val)
	}
	markers := map[string]string{}
	n := 0
	out := placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		n++
		marker := fmt.Sprintf("STAGEPROOFPH%04d", n)
		markers[marker] = m
		return marker
	})
	return out, markers
}

func restore(s string, markers map[string]string) (string, []string) {
	var hit []string
	for marker, token := range markers {
		if strings.Contains(s, marker) {
			hit = append(hit, token)
			s = strings.ReplaceAll(s, marker, fixedPlaceholder)
		}
	}
	sort.Strings(hit)
	return s, unique(hit)
}

func identityErr(label string, tokens []string) error {
	tokens = unique(tokens)
	if len(tokens) == 0 {
		return nil
	}
	return &UnresolvedError{Label: label, Tokens: tokens}
}

func unique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := []string{in[0]}
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
