package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/config"
	"gopkg.in/yaml.v3"
)

type appAdapter struct {
	cfg *config.Config
}

func (a appAdapter) cover(paths ...string) *config.Application {
	if a.cfg == nil {
		return nil
	}
	for i := range a.cfg.Applications {
		app := &a.cfg.Applications[i]
		for _, path := range paths {
			if path != "" && app.CoversFile(path) {
				return app
			}
		}
	}
	return nil
}

func (a appAdapter) Discover(repoRoot string, changedFiles []string) []Target {
	seen := map[string]bool{}
	var out []Target
	for _, f := range changedFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		app := a.cover(f, joinRoot(repoRoot, f))
		if app == nil || seen[app.Name] {
			continue
		}
		seen[app.Name] = true
		out = append(out, Target{Kind: KindApplication, Name: app.Name, Path: app.VersionFrom.File})
	}
	return out
}

func (a appAdapter) Resolve(_ context.Context, target Target, gitRef string) (ResolvedTarget, error) {
	if target.Kind != KindApplication {
		return ResolvedTarget{}, fmt.Errorf("application adapter: %s", target.Kind)
	}
	app := a.byName(target.Name)
	if app == nil {
		return ResolvedTarget{}, fmt.Errorf("application %s is not in .stageproof.yaml", target.Name)
	}
	newVer, err := versionFrom(app.VersionFrom.File, app.VersionFrom.Regex, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	oldVer, err := versionFrom(app.VersionFrom.File, app.VersionFrom.Regex, gitRef)
	if err != nil {
		return ResolvedTarget{}, err
	}
	newVals, err := readValues(a.cfg, app.ValuesFrom, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	oldVals, err := readValues(a.cfg, app.ValuesFrom, gitRef)
	if err != nil {
		return ResolvedTarget{}, err
	}
	tmp, err := os.MkdirTemp("", "sp-app-*")
	if err != nil {
		return ResolvedTarget{}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	newPath := filepath.Join(tmp, "app-new.yaml")
	oldPath := filepath.Join(tmp, "app-current.yaml")
	if err := os.WriteFile(newPath, []byte(applicationYAML(*app, newVer, newVals)), 0o644); err != nil {
		cleanup()
		return ResolvedTarget{}, err
	}
	if err := os.WriteFile(oldPath, []byte(applicationYAML(*app, oldVer, oldVals)), 0o644); err != nil {
		cleanup()
		return ResolvedTarget{}, err
	}
	return ResolvedTarget{
		Adapter:     "argocd",
		ArgoFile:    newPath,
		CurrentArgo: oldPath,
		Cleanup:     cleanup,
	}, nil
}

func (a appAdapter) byName(name string) *config.Application {
	if a.cfg == nil {
		return nil
	}
	for i := range a.cfg.Applications {
		if a.cfg.Applications[i].Name == name {
			return &a.cfg.Applications[i]
		}
	}
	return nil
}

func versionFrom(path, pattern, gitRef string) (string, error) {
	var raw []byte
	var err error
	if gitRef == "" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = readGitFile(gitRef, path)
	}
	if err != nil {
		return "", err
	}
	got, err := config.FirstCapture(string(raw), pattern)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return got, nil
}

func readValues(cfg *config.Config, paths []string, gitRef string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	ph := placeholderMap(cfg)
	var keys []string
	for key := range ph {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	merged := map[string]any{}
	for _, path := range paths {
		var raw []byte
		var err error
		if gitRef == "" {
			raw, err = os.ReadFile(path)
		} else {
			raw, err = readGitFile(gitRef, path)
		}
		if err != nil {
			return "", err
		}
		text := string(raw)
		for _, key := range keys {
			text = strings.ReplaceAll(text, key, ph[key])
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		merged = mergeYAML(merged, doc)
	}
	if len(merged) == 0 {
		return "", nil
	}
	buf, err := yaml.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func mergeYAML(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		srcMap, srcOK := v.(map[string]any)
		dstMap, dstOK := dst[k].(map[string]any)
		if srcOK && dstOK {
			dst[k] = mergeYAML(dstMap, srcMap)
			continue
		}
		dst[k] = v
	}
	return dst
}

func applicationYAML(app config.Application, version, values string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: %s\nspec:\n  source:\n    repoURL: %s\n    chart: %s\n    targetRevision: %s\n    helm:\n      values: |\n", app.Name, app.RepoURL, app.Chart, version)
	for _, line := range strings.Split(values, "\n") {
		b.WriteString("        ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
