// Package adapter finds what a pull request changed and turns each hit into
// a check target. Helm charts, Argo CD Applications, Flux HelmReleases, and
// applications listed in .stageproof.yaml share one discovery order.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/argocd"
	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/flux"
)

// Kind is the input a target came from.
type Kind string

const (
	KindHelm        Kind = "helm"
	KindArgoCD      Kind = "argocd"
	KindFlux        Kind = "flux"
	KindApplication Kind = "application"
)

// Target is one thing Discover found. Path is a chart directory, an
// Application file, or the version file of a configured application.
type Target struct {
	Kind Kind
	Path string
	Name string
}

// ResolvedTarget is what Resolve hands to the check. Cleanup removes any
// temporary files. It is nil when Resolve did not create any.
type ResolvedTarget struct {
	Adapter       string
	HeadChart     string
	Values        []string
	ArgoFile      string
	CurrentArgo   string
	FluxFile      string
	ArgoSkipCrds  *bool
	FluxCRDPolicy string
	Cleanup       func()
}

// Adapter discovers its own targets and resolves one of them.
type Adapter interface {
	Discover(repoRoot string, changedFiles []string) []Target
	Resolve(ctx context.Context, target Target, gitRef string) (ResolvedTarget, error)
}

var (
	_ Adapter = helmAdapter{}
	_ Adapter = argoAdapter{}
	_ Adapter = fluxAdapter{}
	_ Adapter = appAdapter{}
)

// Set runs the adapters in order. An Argo CD file is not also a chart. A
// file covered by applications: is not also a chart. A deleted path is still
// offered to the Helm adapter, because the chart directory can outlive the file.
type Set struct {
	argo argoAdapter
	flux fluxAdapter
	apps appAdapter
	helm helmAdapter
}

// NewWithConfig uses a config that was already loaded. check-pr passes the
// file from the base ref so a pull request cannot replace it.
func NewWithConfig(cfg *config.Config) *Set {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Set{
		argo: argoAdapter{cfg: cfg},
		flux: fluxAdapter{cfg: cfg},
		apps: appAdapter{cfg: cfg},
		helm: helmAdapter{cfg: cfg},
	}
}

// New loads .stageproof.yaml from root when it exists. A missing file is an
// empty config, not an error.
func New(root string) *Set {
	path := ".stageproof.yaml"
	if root != "" && root != "." {
		path = filepath.Join(root, ".stageproof.yaml")
	}
	cfg, err := config.Load(path)
	if err != nil {
		cfg = &config.Config{}
	}
	return &Set{
		argo: argoAdapter{cfg: cfg},
		flux: fluxAdapter{cfg: cfg},
		apps: appAdapter{cfg: cfg},
		helm: helmAdapter{cfg: cfg},
	}
}

// Discover lists Helm charts first, in path order, then Argo CD files and
// configured applications in the order the diff named them.
func (s *Set) Discover(repoRoot string, changedFiles []string) []Target {
	var appFiles, helmFiles []string
	for _, f := range changedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		path := joinRoot(repoRoot, f)
		if s.argo.matches(path) || s.flux.matches(path) {
			continue
		}
		if s.apps.cover(f, path) != nil {
			appFiles = append(appFiles, f)
			continue
		}
		helmFiles = append(helmFiles, f)
	}
	charts := s.helm.Discover(repoRoot, helmFiles)
	// Pass every changed path, not only files that are already Applications or
	// HelmReleases. A values file outside the chart still belongs to the
	// object that names it. Each Discover dedups a file that is itself the object.
	rest := append(s.argo.Discover(repoRoot, changedFiles), s.flux.Discover(repoRoot, changedFiles)...)
	rest = append(rest, s.apps.Discover(repoRoot, appFiles)...)
	return append(charts, rest...)
}

// Resolve dispatches to the adapter that owns the target.
func (s *Set) Resolve(ctx context.Context, target Target, gitRef string) (ResolvedTarget, error) {
	switch target.Kind {
	case KindHelm:
		return s.helm.Resolve(ctx, target, gitRef)
	case KindArgoCD:
		return s.argo.Resolve(ctx, target, gitRef)
	case KindFlux:
		return s.flux.Resolve(ctx, target, gitRef)
	case KindApplication:
		return s.apps.Resolve(ctx, target, gitRef)
	default:
		return ResolvedTarget{}, fmt.Errorf("unknown target %q", target.Kind)
	}
}

func joinRoot(root, rel string) string {
	if root == "" || root == "." || filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(root, rel)
}

func relToRoot(root, path string) string {
	if root == "" || root == "." {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

// chartRoot walks up from path to the nearest Chart.yaml. path may name a
// file that the pull request deleted. A chart under charts/<name> belongs to
// the parent chart when that parent has a Chart.yaml.
func chartRoot(path string) string {
	dir := path
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	var found string
	for dir != "." && dir != "/" && dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err == nil {
			found = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if found == "" {
		return ""
	}
	parentDir := filepath.Dir(found)
	if filepath.Base(parentDir) == "charts" {
		grand := filepath.Dir(parentDir)
		if _, err := os.Stat(filepath.Join(grand, "Chart.yaml")); err == nil {
			return grand
		}
	}
	return found
}

type helmAdapter struct {
	cfg *config.Config
}

func (helmAdapter) Discover(repoRoot string, changedFiles []string) []Target {
	seen := map[string]bool{}
	var out []Target
	for _, f := range changedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		dir := chartRoot(joinRoot(repoRoot, f))
		if dir == "" {
			continue
		}
		rel := relToRoot(repoRoot, dir)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, Target{Kind: KindHelm, Path: rel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (h helmAdapter) Resolve(_ context.Context, target Target, gitRef string) (ResolvedTarget, error) {
	if target.Kind != KindHelm {
		return ResolvedTarget{}, fmt.Errorf("helm adapter: %s", target.Kind)
	}
	if _, err := os.Stat(filepath.Join(target.Path, "Chart.yaml")); err != nil {
		return ResolvedTarget{}, fmt.Errorf("chart %s at %s: %w", target.Path, gitRef, err)
	}
	var values []string
	if h.cfg != nil {
		values = h.cfg.ValuesFilesFor(target.Path)
	}
	if len(values) == 0 {
		if vf := filepath.Join(target.Path, "values.yaml"); fileExists(vf) {
			values = []string{vf}
		}
	}
	return ResolvedTarget{Adapter: "helm", HeadChart: target.Path, Values: values}, nil
}

type argoAdapter struct {
	cfg *config.Config
}

func (a argoAdapter) matches(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
	default:
		if !strings.HasSuffix(path, ".yaml.tmpl") {
			return false
		}
	}
	_, _, err := argocd.ParseApplicationFile(path, placeholderMap(a.cfg))
	if err == nil || len(argocd.UnresolvedTokens(err)) > 0 {
		return true
	}
	var skipped *argocd.NotCheckedError
	return errors.As(err, &skipped)
}

func (a argoAdapter) Discover(repoRoot string, changedFiles []string) []Target {
	seen := map[string]bool{}
	var out []Target
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, Target{Kind: KindArgoCD, Path: path})
	}
	for _, f := range changedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		path := joinRoot(repoRoot, f)
		if a.matches(path) {
			add(path)
		}
	}
	// A values file or a chart file can change without the Application file
	// itself changing. Match those paths. Do not stat the changed path first:
	// a deleted values file is still a change.
	for _, path := range argocd.ApplicationsReferencing(repoRoot, changedFiles) {
		add(path)
	}
	return out
}

func (a argoAdapter) Resolve(_ context.Context, target Target, gitRef string) (ResolvedTarget, error) {
	if target.Kind != KindArgoCD {
		return ResolvedTarget{}, fmt.Errorf("argocd adapter: %s", target.Kind)
	}
	if _, err := os.Stat(target.Path); err != nil {
		return ResolvedTarget{}, fmt.Errorf("application %s at %s: %w", target.Path, gitRef, err)
	}
	return ResolvedTarget{Adapter: "argocd", ArgoFile: target.Path}, nil
}

type fluxAdapter struct {
	cfg *config.Config
}

func (a fluxAdapter) matches(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
	default:
		if !strings.HasSuffix(path, ".yaml.tmpl") {
			return false
		}
	}
	_, _, err := flux.ParseFile(path, placeholderMap(a.cfg))
	if err == nil || len(flux.UnresolvedTokens(err)) > 0 {
		return true
	}
	var skipped *flux.NotCheckedError
	if errors.As(err, &skipped) {
		return true
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		return false
	}
	text := string(raw)
	return strings.Contains(text, "kind: HelmRelease") && strings.Contains(text, "helm.toolkit.fluxcd.io/")
}

func (a fluxAdapter) Discover(repoRoot string, changedFiles []string) []Target {
	seen := map[string]bool{}
	var out []Target
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, Target{Kind: KindFlux, Path: path})
	}
	for _, f := range changedFiles {
		path := joinRoot(repoRoot, f)
		if a.matches(path) {
			add(relToRoot(repoRoot, path))
		}
	}
	for _, path := range flux.ReleasesReferencing(repoRoot, changedFiles) {
		add(path)
	}
	return out
}

func (a fluxAdapter) Resolve(_ context.Context, target Target, gitRef string) (ResolvedTarget, error) {
	if target.Kind != KindFlux {
		return ResolvedTarget{}, fmt.Errorf("flux adapter: %s", target.Kind)
	}
	if _, err := os.Stat(target.Path); err != nil {
		return ResolvedTarget{}, fmt.Errorf("HelmRelease %s at %s: %w", target.Path, gitRef, err)
	}
	return ResolvedTarget{Adapter: "flux", FluxFile: target.Path}, nil
}

func placeholderMap(cfg *config.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	return cfg.TemplatePlaceholders
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
