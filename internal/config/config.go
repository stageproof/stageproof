package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

type Config struct {
	CurrentKubernetes    string            `yaml:"currentKubernetes"`
	NextKubernetes       string            `yaml:"nextKubernetes,omitempty"`
	ServerSideApply      bool              `yaml:"serverSideApply,omitempty"`
	LiveTest             bool              `yaml:"liveTest"`
	RequireRehearsal     bool              `yaml:"requireRehearsal,omitempty"`
	Charts               []Chart           `yaml:"charts"`
	Applications         []Application     `yaml:"applications,omitempty"`
	TemplatePlaceholders map[string]string `yaml:"templatePlaceholders,omitempty"`
	CRDFiles             []string          `yaml:"crdFiles,omitempty"`

	// Legacy aliases accepted when loading older configs
	TargetKubernetes string   `yaml:"targetKubernetes,omitempty"`
	KubeVersion      string   `yaml:"kubeVersion,omitempty"`
	NextKubeVersion  string   `yaml:"nextKubeVersion,omitempty"`
	Rehearse         bool     `yaml:"rehearse,omitempty"`
	CRDBundles       []string `yaml:"crdBundles,omitempty"`
}

type Chart struct {
	Name        string   `yaml:"name"`
	Path        string   `yaml:"path"`
	ValuesFiles []string `yaml:"valuesFiles,omitempty"`
}

// Application is a Helm chart whose version and values live in other files,
// for example a Terraform variable and a values template.
type Application struct {
	Name        string    `yaml:"name"`
	RepoURL     string    `yaml:"repoURL"`
	Chart       string    `yaml:"chart"`
	VersionFrom FileMatch `yaml:"versionFrom"`
	ValuesFrom  []string  `yaml:"valuesFrom,omitempty"`
}

type FileMatch struct {
	File  string `yaml:"file"`
	Regex string `yaml:"regex"`
}

// FirstCapture returns the first parenthesized group of pattern in text.
func FirstCapture(text, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	m := re.FindStringSubmatch(text)
	if len(m) < 2 || m[1] == "" {
		return "", fmt.Errorf("pattern did not capture a value")
	}
	return m[1], nil
}

// CoversFile reports whether path is one of the files this application is read from.
func (a Application) CoversFile(path string) bool {
	path = filepath.ToSlash(path)
	if filepath.ToSlash(a.VersionFrom.File) == path {
		return true
	}
	for _, vf := range a.ValuesFrom {
		if filepath.ToSlash(vf) == path {
			return true
		}
	}
	return false
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.CurrentKubernetes == "" {
		switch {
		case cfg.KubeVersion != "":
			cfg.CurrentKubernetes = cfg.KubeVersion
		case cfg.TargetKubernetes != "":
			cfg.CurrentKubernetes = cfg.TargetKubernetes
		default:
			cfg.CurrentKubernetes = "1.33"
		}
	}
	if cfg.NextKubernetes == "" && cfg.NextKubeVersion != "" {
		cfg.NextKubernetes = cfg.NextKubeVersion
	}
	if !cfg.LiveTest && cfg.Rehearse {
		cfg.LiveTest = true
	}
	if len(cfg.CRDFiles) == 0 && len(cfg.CRDBundles) > 0 {
		cfg.CRDFiles = cfg.CRDBundles
	}
	return &cfg, nil
}

// ValuesFilesFor returns the values files listed for the chart at chartPath
// that exist on disk. Each one is a separate deployment of that chart, for
// example values-prod.yaml and values-dev.yaml, so each is checked on its own.
func (c *Config) ValuesFilesFor(chartPath string) []string {
	if c == nil {
		return nil
	}
	want := filepath.ToSlash(filepath.Clean(chartPath))
	for _, ch := range c.Charts {
		if filepath.ToSlash(filepath.Clean(ch.Path)) != want {
			continue
		}
		var found []string
		for _, vf := range ch.ValuesFiles {
			if _, err := os.Stat(vf); err == nil {
				found = append(found, vf)
			}
		}
		return found
	}
	return nil
}

// Discover scans rootDir for Chart.yaml files and adjacent values*.yaml overlays.
func Discover(rootDir string) (*Config, error) {
	cfg := &Config{
		CurrentKubernetes: "1.33",
		NextKubernetes:    "1.34",
		ServerSideApply:   false,
		LiveTest:          false,
	}

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == rootDir {
				return nil
			}
			switch d.Name() {
			case ".git", "node_modules", "moved":
				return filepath.SkipDir
			case "charts":
				// charts/ inside a chart holds its subcharts, which belong to the parent.
				// A charts/ directory anywhere else holds the charts themselves.
				if _, cErr := os.Stat(filepath.Join(filepath.Dir(path), "Chart.yaml")); cErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "Chart.yaml" {
			return nil
		}

		chartDir := filepath.Dir(path)
		relDir, relErr := filepath.Rel(rootDir, chartDir)
		if relErr != nil {
			relDir = chartDir
		}

		var chartMeta struct {
			Name string `yaml:"name"`
			Type string `yaml:"type"`
		}
		if raw, rErr := os.ReadFile(path); rErr == nil {
			_ = yaml.Unmarshal(raw, &chartMeta)
		}
		if chartMeta.Type == "library" {
			return nil
		}
		if chartMeta.Name == "" {
			chartMeta.Name = filepath.Base(chartDir)
		}

		overlays := findValuesOverlays(chartDir, rootDir)
		cfg.Charts = append(cfg.Charts, Chart{
			Name:        chartMeta.Name,
			Path:        filepath.ToSlash(relDir),
			ValuesFiles: overlays,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(cfg.Charts, func(i, j int) bool {
		return cfg.Charts[i].Path < cfg.Charts[j].Path
	})
	return cfg, nil
}

func findValuesOverlays(chartDir, rootDir string) []string {
	var found []string
	entries, err := os.ReadDir(chartDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			if n == "values.yaml" || (len(n) > 11 && n[:7] == "values-" && filepath.Ext(n) == ".yaml") {
				rel, rErr := filepath.Rel(rootDir, filepath.Join(chartDir, n))
				if rErr == nil {
					found = append(found, filepath.ToSlash(rel))
				}
			}
		}
	}
	ciDir := filepath.Join(chartDir, "ci")
	if ciEntries, ciErr := os.ReadDir(ciDir); ciErr == nil {
		for _, e := range ciEntries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
				rel, rErr := filepath.Rel(rootDir, filepath.Join(ciDir, e.Name()))
				if rErr == nil {
					found = append(found, filepath.ToSlash(rel))
				}
			}
		}
	}
	sort.Strings(found)
	return found
}

func Write(path string, cfg *Config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# Written by `stageproof init`. Settings for `stageproof check` and `stageproof check-pr`.\n"
	return os.WriteFile(path, append([]byte(header), b...), 0o644)
}
