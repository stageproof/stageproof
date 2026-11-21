package flux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/argocd"
	"gopkg.in/yaml.v3"
)

// MaterializeOptions selects the checkout a Git path or a values object is read from.
// GitRef empty is the working tree. Remotes, when non-nil, replaces `git remote`.
type MaterializeOptions struct {
	RepoRoot     string
	GitRef       string
	Remotes      []string
	Placeholders map[string]string
}

// Materialized is one side of a HelmRelease, ready for helm template.
type Materialized struct {
	ChartDir    string
	ValueFiles  []string
	Labels      map[string]string
	ReleaseName string
	Namespace   string
	CRDPolicy   string
	Gaps        []string
	RefLabel    string
	Used        []string
	cleanup     func()
}

// Cleanup removes temporary chart unpacks and generated values files.
func (m *Materialized) Cleanup() {
	if m != nil && m.cleanup != nil {
		m.cleanup()
	}
}

type chartPlan struct {
	kind          string
	repoURL       string
	chart         string
	version       string
	insecure      bool
	localPath     string
	valuesFiles   []string
	ignoreMissing bool
	valuesInRepo  bool
}

// Materialize resolves the chart and the HelmRelease values.
// A GitRepository on another remote returns NotCheckedError before any pull.
// A missing chart values file returns NotCheckedError when ignoreMissingValuesFiles
// is false, and the chart is not rendered from its defaults.
// A missing valuesFrom object is listed in Gaps. The chart is still returned.
func Materialize(ctx context.Context, rel *Release, opt MaterializeOptions) (mat *Materialized, err error) {
	if rel == nil {
		return nil, errors.New("missing HelmRelease")
	}
	root := opt.RepoRoot
	if root == "" {
		root, err = gitTop()
		if err != nil {
			return nil, err
		}
	}
	idx, err := buildIndex(root, opt.GitRef)
	if err != nil {
		return nil, err
	}
	idx.placeholders = opt.Placeholders
	remotes := opt.Remotes
	plan, err := resolveChart(rel, idx, remotes, root)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "stageproof-flux-*")
	if err != nil {
		return nil, err
	}
	mat = &Materialized{
		ReleaseName: ReleaseName(rel.TargetNamespace, rel.Name, rel.ReleaseName),
		Namespace:   releaseNamespace(rel),
		CRDPolicy:   rel.UpgradeCRDs,
		Labels:      map[string]string{},
		RefLabel:    plan.label(),
		Used:        append([]string(nil), rel.Used...),
		cleanup:     func() { _ = os.RemoveAll(tmp) },
	}
	defer func() {
		if err != nil {
			mat.Cleanup()
			mat = nil
		}
	}()

	switch plan.kind {
	case "git":
		relPath, relErr := cleanRel(plan.localPath)
		if relErr != nil {
			return nil, &NotCheckedError{Reason: rel.Label + " chart path escapes the repository."}
		}
		if opt.GitRef == "" {
			mat.ChartDir = filepath.Join(root, filepath.FromSlash(relPath))
		} else {
			dir, arcErr := archiveInto(tmp, root, opt.GitRef, relPath)
			if arcErr != nil {
				return nil, arcErr
			}
			mat.ChartDir = dir
		}
		if _, statErr := os.Stat(filepath.Join(mat.ChartDir, "Chart.yaml")); statErr != nil {
			return nil, &NotCheckedError{Reason: fmt.Sprintf("%s path %s has no Chart.yaml. The chart was not rendered from defaults.", rel.Label, relPath)}
		}
		// valuesFiles are written into values.yaml. A checkout with no git
		// revision is the working tree, so copy the chart before that write.
		if opt.GitRef == "" && len(plan.valuesFiles) > 0 {
			dest := filepath.Join(tmp, "chart")
			if err = copyTree(mat.ChartDir, dest); err != nil {
				return nil, err
			}
			mat.ChartDir = dest
		}
	case "oci":
		dir, pullErr := pullOCI(ctx, plan.repoURL, plan.version, filepath.Join(tmp, "chart"), plan.insecure)
		if pullErr != nil {
			return nil, pullErr
		}
		mat.ChartDir = dir
	default:
		dir, pullErr := argocd.PullChart(ctx, plan.repoURL, plan.chart, plan.version, filepath.Join(tmp, "chart"))
		if pullErr != nil {
			return nil, pullErr
		}
		mat.ChartDir = dir
	}

	if err = applyChartValues(plan, root, opt.GitRef, mat.ChartDir); err != nil {
		return nil, err
	}
	merged, gaps, used, vErr := releaseValues(rel, idx, mat.Namespace)
	if vErr != nil {
		return nil, vErr
	}
	mat.Gaps = gaps
	mat.Used = unique(append(mat.Used, used...))
	if len(merged) > 0 {
		buf, mErr := yaml.Marshal(merged)
		if mErr != nil {
			return nil, mErr
		}
		path := filepath.Join(tmp, "helmrelease-values.yaml")
		if wErr := os.WriteFile(path, buf, 0o600); wErr != nil {
			return nil, wErr
		}
		mat.ValueFiles = []string{path}
		mat.Labels[path] = valuesLabel(rel)
	}
	return mat, nil
}

func (p chartPlan) label() string {
	switch p.kind {
	case "git":
		return p.localPath
	case "oci":
		if p.version == "" {
			return p.repoURL
		}
		return p.repoURL + ":" + p.version
	default:
		return p.chart + "@" + p.version
	}
}

func releaseNamespace(rel *Release) string {
	if strings.TrimSpace(rel.TargetNamespace) != "" {
		return rel.TargetNamespace
	}
	if strings.TrimSpace(rel.Namespace) != "" {
		return rel.Namespace
	}
	return "default"
}

func resolveChart(rel *Release, idx *objectIndex, remotes []string, root string) (chartPlan, error) {
	if rel.Chart.Mode == "ref" {
		return resolveRef(rel, idx, remotes, root)
	}
	if rel.Chart.TemplateKind == "OCIRepository" {
		version, err := ociVersion(rel.Chart.OCI.Tag, rel.Chart.OCI.Semver, rel.Chart.OCI.Digest)
		if err != nil {
			return chartPlan{}, err
		}
		return chartPlan{kind: "oci", repoURL: rel.Chart.OCI.URL, version: version, insecure: rel.Chart.OCI.Insecure}, nil
	}
	return resolveSource(rel.Label, rel.Chart.SourceRef, rel.Chart.Chart, rel.Chart.Version, rel.Chart.ValuesFiles, rel.Chart.IgnoreMissing, releaseNamespace(rel), idx, remotes, root)
}

func resolveRef(rel *Release, idx *objectIndex, remotes []string, root string) (chartPlan, error) {
	ns := rel.Chart.Ref.Namespace
	if ns == "" {
		ns = releaseNamespace(rel)
	}
	switch rel.Chart.Ref.Kind {
	case "OCIRepository":
		raw, _, err := idx.lookup("OCIRepository", ns, rel.Chart.Ref.Name)
		if err != nil {
			return chartPlan{}, err
		}
		oci, err := parseOCI(raw, rel.Label, idx.placeholders)
		if err != nil {
			return chartPlan{}, err
		}
		version, err := ociVersion(oci.Tag, oci.Semver, oci.Digest)
		if err != nil {
			return chartPlan{}, err
		}
		return chartPlan{kind: "oci", repoURL: oci.URL, version: version, insecure: oci.Insecure}, nil
	case "HelmChart":
		raw, _, err := idx.lookup("HelmChart", ns, rel.Chart.Ref.Name)
		if err != nil {
			return chartPlan{}, err
		}
		spec, err := parseHelmChart(raw, rel.Label, idx.placeholders)
		if err != nil {
			return chartPlan{}, err
		}
		return resolveSource(rel.Label, spec.source, spec.chart, spec.version, spec.valuesFiles, spec.ignoreMissing, ns, idx, remotes, root)
	default:
		return chartPlan{}, &NotCheckedError{Reason: rel.Label + " chartRef kind " + rel.Chart.Ref.Kind + " is not read."}
	}
}

func resolveSource(label string, ref ObjectRef, chartName, version string, valuesFiles []string, ignore bool, ns string, idx *objectIndex, remotes []string, root string) (chartPlan, error) {
	if ref.Namespace != "" {
		ns = ref.Namespace
	}
	switch ref.Kind {
	case "HelmRepository":
		raw, _, err := idx.lookup("HelmRepository", ns, ref.Name)
		if err != nil {
			return chartPlan{}, missingSource(label, ref, ns, err)
		}
		repo, err := parseHelmRepository(raw, label, idx.placeholders)
		if err != nil {
			return chartPlan{}, err
		}
		if version == "" {
			version = "*"
		}
		url := repo.url
		if repo.kind == "oci" && !strings.HasPrefix(url, "oci://") {
			url = "oci://" + strings.TrimPrefix(url, "/")
		}
		return chartPlan{
			kind: "helm", repoURL: url, chart: chartName, version: version, insecure: repo.insecure,
			valuesFiles: valuesFiles, ignoreMissing: ignore,
		}, nil
	case "OCIRepository":
		raw, _, err := idx.lookup("OCIRepository", ns, ref.Name)
		if err != nil {
			return chartPlan{}, missingSource(label, ref, ns, err)
		}
		oci, err := parseOCI(raw, label, idx.placeholders)
		if err != nil {
			return chartPlan{}, err
		}
		ver, err := ociVersion(oci.Tag, oci.Semver, oci.Digest)
		if err != nil {
			return chartPlan{}, err
		}
		if ver == "" {
			ver = version
		}
		return chartPlan{kind: "oci", repoURL: oci.URL, version: ver, insecure: oci.Insecure}, nil
	case "GitRepository":
		raw, _, err := idx.lookup("GitRepository", ns, ref.Name)
		if err != nil {
			return chartPlan{}, missingSource(label, ref, ns, err)
		}
		repoURL, err := parseGitURL(raw, label, idx.placeholders)
		if err != nil {
			return chartPlan{}, err
		}
		if remotes == nil && root != "" {
			var rErr error
			remotes, rErr = gitRemotes(root)
			if rErr != nil {
				return chartPlan{}, rErr
			}
		}
		if !sameRepo(repoURL, remotes) {
			return chartPlan{}, &NotCheckedError{Reason: fmt.Sprintf("GitRepository %s is %s, which is not a remote of this checkout. Stageproof does not clone other Git repositories.", ref.Name, repoURL)}
		}
		return chartPlan{
			kind: "git", repoURL: repoURL, localPath: chartName,
			valuesFiles: valuesFiles, ignoreMissing: ignore, valuesInRepo: true,
		}, nil
	case "Bucket":
		return chartPlan{}, &NotCheckedError{Reason: label + " sourceRef kind Bucket is not read. Stageproof does not fetch a bucket."}
	default:
		return chartPlan{}, &NotCheckedError{Reason: label + " sourceRef kind " + ref.Kind + " is not read."}
	}
}

func missingSource(label string, ref ObjectRef, namespace string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return &NotCheckedError{Reason: fmt.Sprintf("%s %s %s/%s is not in this repository. Stageproof does not invent a chart repository and does not render the chart from defaults.", label, ref.Kind, namespaceOrDefault(namespace), ref.Name)}
	}
	return err
}

func ociVersion(tag, semver, digest string) (string, error) {
	n := 0
	if tag != "" {
		n++
	}
	if semver != "" {
		n++
	}
	if digest != "" {
		n++
	}
	if n > 1 {
		return "", &NotCheckedError{Reason: "OCIRepository ref sets more than one of tag, semver, and digest."}
	}
	if digest != "" {
		return "@" + digest, nil
	}
	if tag != "" {
		return tag, nil
	}
	return semver, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

func applyChartValues(plan chartPlan, repoRoot, gitRef, chartDir string) error {
	if len(plan.valuesFiles) == 0 {
		return nil
	}
	merged := map[string]any{}
	used := false
	for _, file := range plan.valuesFiles {
		rel, err := cleanRel(file)
		if err != nil {
			return &NotCheckedError{Reason: "values file " + file + " escapes its source."}
		}
		raw, err := readValuesFile(plan, repoRoot, gitRef, chartDir, rel)
		if err != nil {
			if plan.ignoreMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if errors.Is(err, os.ErrNotExist) {
				return &NotCheckedError{Reason: "values file " + file + " is missing. The chart was not rendered from its defaults."}
			}
			return err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("parse values file %s: %w", file, err)
		}
		merged = mergeMaps(merged, doc)
		used = true
	}
	if !used {
		return nil
	}
	buf, err := yaml.Marshal(merged)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(chartDir, "values.yaml"), buf, 0o644)
}

func readValuesFile(plan chartPlan, repoRoot, gitRef, chartDir, rel string) ([]byte, error) {
	if plan.valuesInRepo {
		if gitRef == "" {
			abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
			if !within(repoRoot, abs) {
				return nil, &NotCheckedError{Reason: "values file " + rel + " escapes the repository."}
			}
			return os.ReadFile(abs)
		}
		return gitShow(repoRoot, gitRef, rel)
	}
	abs := filepath.Join(chartDir, filepath.FromSlash(rel))
	if !within(chartDir, abs) {
		return nil, &NotCheckedError{Reason: "values file " + rel + " escapes the chart."}
	}
	return os.ReadFile(abs)
}

func releaseValues(rel *Release, idx *objectIndex, namespace string) (map[string]any, []string, []string, error) {
	var parts []piece
	var gaps []string
	var used []string
	for _, ref := range rel.ValuesFrom {
		raw, found, err := idx.lookup(ref.Kind, namespace, ref.Name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, err
		}
		if errors.Is(err, os.ErrNotExist) || !found {
			if ref.Optional {
				continue
			}
			key := ref.ValuesKey
			if key == "" {
				key = "values.yaml"
			}
			gaps = append(gaps, fmt.Sprintf("%s/%s/%s key %s is not in this repository.", ref.Kind, namespaceOrDefault(namespace), ref.Name, key))
			continue
		}
		text, markers := substitute(string(raw), idx.placeholders)
		restored, hit := restore(text, markers)
		used = append(used, hit...)
		part, gap, rErr := readObjectValues([]byte(restored), ref, namespace)
		if rErr != nil {
			return nil, nil, nil, rErr
		}
		if gap != "" {
			gaps = append(gaps, gap)
			continue
		}
		parts = append(parts, part)
	}
	sort.Strings(gaps)
	return mergeValues(parts, rel.Inline), gaps, unique(used), nil
}

func valuesLabel(rel *Release) string {
	if len(rel.ValuesFrom) == 0 {
		return "HelmRelease spec.values"
	}
	var names []string
	for _, ref := range rel.ValuesFrom {
		names = append(names, ref.Kind+"/"+ref.Name)
	}
	return "HelmRelease values (" + strings.Join(names, ", ") + ")"
}

type helmRepo struct {
	url, kind string
	insecure  bool
}

type helmChartSpec struct {
	chart, version string
	source         ObjectRef
	valuesFiles    []string
	ignoreMissing  bool
}

func parseHelmRepository(raw []byte, label string, ph map[string]string) (helmRepo, error) {
	text, markers := substitute(string(raw), ph)
	var doc struct {
		Spec struct {
			URL      string `yaml:"url"`
			Type     string `yaml:"type"`
			Insecure bool   `yaml:"insecure"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return helmRepo{}, fmt.Errorf("parse HelmRepository %s: %w", label, err)
	}
	url, hit := restore(doc.Spec.URL, markers)
	if len(hit) > 0 {
		return helmRepo{}, &UnresolvedError{Label: label, Tokens: hit}
	}
	return helmRepo{url: strings.TrimSpace(url), kind: doc.Spec.Type, insecure: doc.Spec.Insecure}, nil
}

func parseGitURL(raw []byte, label string, ph map[string]string) (string, error) {
	text, markers := substitute(string(raw), ph)
	var doc struct {
		Spec struct {
			URL string `yaml:"url"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "", fmt.Errorf("parse GitRepository %s: %w", label, err)
	}
	url, hit := restore(doc.Spec.URL, markers)
	if len(hit) > 0 {
		return "", &UnresolvedError{Label: label, Tokens: hit}
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return "", &NotCheckedError{Reason: label + " GitRepository has no spec.url."}
	}
	return url, nil
}

func parseOCI(raw []byte, label string, ph map[string]string) (OCIRef, error) {
	text, markers := substitute(string(raw), ph)
	var doc struct {
		Spec struct {
			URL      string `yaml:"url"`
			Insecure bool   `yaml:"insecure"`
			Ref      struct {
				Tag    string `yaml:"tag"`
				Semver string `yaml:"semver"`
				Digest string `yaml:"digest"`
			} `yaml:"ref"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return OCIRef{}, fmt.Errorf("parse OCIRepository %s: %w", label, err)
	}
	url, urlHit := restore(doc.Spec.URL, markers)
	tag, tagHit := restore(doc.Spec.Ref.Tag, markers)
	semver, semHit := restore(doc.Spec.Ref.Semver, markers)
	digest, digHit := restore(doc.Spec.Ref.Digest, markers)
	hit := unique(append(append(append(urlHit, tagHit...), semHit...), digHit...))
	if len(hit) > 0 {
		return OCIRef{}, &UnresolvedError{Label: label, Tokens: hit}
	}
	return OCIRef{
		URL: strings.TrimSpace(url), Tag: strings.TrimSpace(tag), Semver: strings.TrimSpace(semver),
		Digest: strings.TrimSpace(digest), Insecure: doc.Spec.Insecure,
	}, nil
}

func parseHelmChart(raw []byte, label string, ph map[string]string) (helmChartSpec, error) {
	text, markers := substitute(string(raw), ph)
	var doc struct {
		Spec struct {
			Chart                    string   `yaml:"chart"`
			Version                  string   `yaml:"version"`
			ValuesFiles              []string `yaml:"valuesFiles"`
			IgnoreMissingValuesFiles bool     `yaml:"ignoreMissingValuesFiles"`
			SourceRef                struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
				Name       string `yaml:"name"`
				Namespace  string `yaml:"namespace"`
			} `yaml:"sourceRef"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return helmChartSpec{}, fmt.Errorf("parse HelmChart %s: %w", label, err)
	}
	chart, chartHit := restore(doc.Spec.Chart, markers)
	version, verHit := restore(doc.Spec.Version, markers)
	kind, kindHit := restore(doc.Spec.SourceRef.Kind, markers)
	name, nameHit := restore(doc.Spec.SourceRef.Name, markers)
	hit := unique(append(append(append(chartHit, verHit...), kindHit...), nameHit...))
	var files []string
	for _, file := range doc.Spec.ValuesFiles {
		got, fileHit := restore(file, markers)
		files = append(files, strings.TrimSpace(got))
		hit = unique(append(hit, fileHit...))
	}
	if len(hit) > 0 {
		return helmChartSpec{}, &UnresolvedError{Label: label, Tokens: hit}
	}
	return helmChartSpec{
		chart: strings.TrimSpace(chart), version: strings.TrimSpace(version),
		valuesFiles: files, ignoreMissing: doc.Spec.IgnoreMissingValuesFiles,
		source: ObjectRef{Kind: strings.TrimSpace(kind), Name: strings.TrimSpace(name), Namespace: doc.Spec.SourceRef.Namespace},
	}, nil
}

type objectIndex struct {
	items        map[string][]indexedObj
	placeholders map[string]string
}

type indexedObj struct {
	namespace string
	raw       []byte
	path      string
}

func (idx *objectIndex) lookup(kind, namespace, name string) ([]byte, bool, error) {
	if idx == nil {
		return nil, false, os.ErrNotExist
	}
	hits := idx.items[kind+"/"+name]
	if len(hits) == 0 {
		return nil, false, os.ErrNotExist
	}
	ns := namespaceOrDefault(namespace)
	var exact, blank []indexedObj
	for _, hit := range hits {
		switch hit.namespace {
		case ns:
			exact = append(exact, hit)
		case "":
			blank = append(blank, hit)
		}
	}
	chosen := exact
	if len(chosen) == 0 {
		chosen = blank
	}
	if len(chosen) == 0 {
		return nil, false, os.ErrNotExist
	}
	if len(chosen) > 1 {
		var paths []string
		for _, hit := range chosen {
			paths = append(paths, hit.path)
		}
		sort.Strings(paths)
		return nil, false, &NotCheckedError{Reason: fmt.Sprintf("%s %s/%s is declared in more than one file: %s.", kind, ns, name, strings.Join(paths, ", "))}
	}
	return chosen[0].raw, true, nil
}

func (idx *objectIndex) add(raw []byte, file string) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			return
		}
		var doc struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := node.Decode(&doc); err != nil || doc.Kind == "" || doc.Metadata.Name == "" {
			continue
		}
		switch doc.Kind {
		case "HelmRepository", "OCIRepository", "GitRepository", "HelmChart":
			if !strings.HasPrefix(doc.APIVersion, "source.toolkit.fluxcd.io/") {
				continue
			}
		case "ConfigMap", "Secret":
			if doc.APIVersion != "v1" {
				continue
			}
		default:
			continue
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		if err := enc.Encode(&node); err != nil {
			_ = enc.Close()
			continue
		}
		if err := enc.Close(); err != nil {
			continue
		}
		idx.items[doc.Kind+"/"+doc.Metadata.Name] = append(idx.items[doc.Kind+"/"+doc.Metadata.Name], indexedObj{
			namespace: doc.Metadata.Namespace,
			raw:       append([]byte(nil), buf.Bytes()...),
			path:      file,
		})
	}
}

func buildIndex(root, gitRef string) (*objectIndex, error) {
	idx := &objectIndex{items: map[string][]indexedObj{}}
	var files []string
	if gitRef == "" {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !yamlName(d.Name()) {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil || info.Size() > 2<<20 {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		listed, err := gitList(root, gitRef)
		if err != nil {
			return nil, err
		}
		files = listed
	}
	for _, rel := range files {
		if !yamlName(rel) || skippedPath(rel) {
			continue
		}
		var raw []byte
		var err error
		if gitRef == "" {
			raw, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		} else {
			raw, err = gitShow(root, gitRef, rel)
		}
		if err != nil || len(raw) > 2<<20 {
			continue
		}
		idx.add(raw, rel)
	}
	return idx, nil
}

// ReleasesReferencing returns HelmRelease files whose chart source, chart path,
// or valuesFrom object includes one of changed. A deleted path is still matched
// when git can show the file that was removed.
func ReleasesReferencing(repoRoot string, changed []string, ph map[string]string) []string {
	if repoRoot == "" {
		repoRoot = "."
	}
	want := map[string]bool{}
	for _, f := range changed {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		want[relChanged(repoRoot, f)] = true
	}
	if len(want) == 0 {
		return nil
	}
	idx, err := buildIndex(repoRoot, "")
	if err != nil {
		return nil
	}
	idx.placeholders = ph
	for rel := range want {
		if _, statErr := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel))); statErr == nil {
			continue
		}
		raw, readErr := deletedBytes(repoRoot, rel)
		if readErr == nil {
			idx.add(raw, rel)
		}
	}
	var found []string
	_ = filepath.WalkDir(repoRoot, func(abs string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !yamlName(d.Name()) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > 2<<20 {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, abs)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		relObj, _, parseErr := ParseFile(abs, ph)
		if parseErr != nil || relObj == nil {
			return nil
		}
		if want[rel] || releaseTouches(relObj, idx, want, repoRoot) {
			found = append(found, rel)
		}
		return nil
	})
	for rel := range want {
		if _, statErr := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel))); statErr == nil {
			continue
		}
		raw, readErr := deletedBytes(repoRoot, rel)
		if readErr != nil {
			continue
		}
		if _, _, parseErr := ParseBytes(raw, rel, ph); parseErr == nil || len(UnresolvedTokens(parseErr)) > 0 || isNotChecked(parseErr) {
			found = append(found, rel)
		}
	}
	sort.Strings(found)
	return unique(found)
}

func isNotChecked(err error) bool {
	var skipped *NotCheckedError
	return errors.As(err, &skipped)
}

func releaseTouches(rel *Release, idx *objectIndex, want map[string]bool, root string) bool {
	ns := releaseNamespace(rel)
	if rel.Chart.Mode == "ref" {
		if path, ok := objectPath(idx, rel.Chart.Ref.Kind, firstNonEmpty(rel.Chart.Ref.Namespace, ns), rel.Chart.Ref.Name); ok && want[path] {
			return true
		}
		if rel.Chart.Ref.Kind == "HelmChart" {
			if raw, _, err := idx.lookup("HelmChart", firstNonEmpty(rel.Chart.Ref.Namespace, ns), rel.Chart.Ref.Name); err == nil {
				if spec, err := parseHelmChart(raw, rel.Label, idx.placeholders); err == nil {
					if path, ok := objectPath(idx, spec.source.Kind, firstNonEmpty(spec.source.Namespace, ns), spec.source.Name); ok && want[path] {
						return true
					}
					for _, file := range spec.valuesFiles {
						if want[file] {
							return true
						}
					}
					if spec.source.Kind == "GitRepository" && pathTouched(want, spec.chart) {
						return true
					}
				}
			}
		}
	} else if rel.Chart.TemplateKind == "HelmChart" || rel.Chart.TemplateKind == "" {
		ref := rel.Chart.SourceRef
		if path, ok := objectPath(idx, ref.Kind, firstNonEmpty(ref.Namespace, ns), ref.Name); ok && want[path] {
			return true
		}
		for _, file := range rel.Chart.ValuesFiles {
			if want[file] {
				return true
			}
		}
		if ref.Kind == "GitRepository" && pathTouched(want, rel.Chart.Chart) {
			return true
		}
	}
	for _, v := range rel.ValuesFrom {
		if path, ok := objectPath(idx, v.Kind, ns, v.Name); ok && want[path] {
			return true
		}
	}
	return false
}

func objectPath(idx *objectIndex, kind, namespace, name string) (string, bool) {
	rawPath := ""
	hits := idx.items[kind+"/"+name]
	ns := namespaceOrDefault(namespace)
	for _, hit := range hits {
		if hit.namespace == ns || hit.namespace == "" {
			if rawPath == "" || hit.namespace == ns {
				rawPath = hit.path
			}
		}
	}
	return rawPath, rawPath != ""
}

func pathTouched(want map[string]bool, chartPath string) bool {
	clean, err := cleanRel(chartPath)
	if err != nil {
		return false
	}
	prefix := strings.TrimSuffix(clean, "/") + "/"
	for file := range want {
		if file == clean || strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func deletedBytes(root, rel string) ([]byte, error) {
	if raw, err := gitShow(root, "HEAD", rel); err == nil {
		return raw, nil
	}
	rev, err := gitOut(root, "log", "-1", "--format=%H", "--", rel)
	if err != nil {
		return nil, err
	}
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return nil, os.ErrNotExist
	}
	return gitShow(root, rev+"^", rel)
}

func pullOCI(ctx context.Context, repoURL, version, dest string, insecure bool) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	ref := strings.TrimRight(repoURL, "/")
	args := []string{"pull", ref, "--untar", "--untardir", dest}
	if version != "" {
		if strings.HasPrefix(version, "@") {
			args = []string{"pull", ref + version, "--untar", "--untardir", dest}
		} else {
			args = append(args, "--version", version)
		}
	}
	if insecure {
		args = append(args, "--plain-http")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helm %s failed: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("helm pull unpacked %d directories in %s", len(dirs), dest)
	}
	return filepath.Join(dest, dirs[0]), nil
}

func cleanRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	if p == "." || p == "" || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return "", errors.New("path escapes")
	}
	return p, nil
}

func within(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func yamlName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml.tmpl")
}

func skippedPath(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" || part == "vendor" || part == "node_modules" {
			return true
		}
	}
	return false
}

func relChanged(root, f string) string {
	f = filepath.Clean(f)
	if filepath.IsAbs(f) {
		if rel, err := filepath.Rel(root, f); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(f)
}

func sameRepo(repoURL string, remotes []string) bool {
	want := canonicalRepo(repoURL)
	if want == "" {
		return false
	}
	for _, remote := range remotes {
		if canonicalRepo(remote) == want {
			return true
		}
	}
	return false
}

func canonicalRepo(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if strings.HasPrefix(s, "git@") {
		s = strings.TrimPrefix(s, "git@")
		host, path, ok := strings.Cut(s, ":")
		if ok {
			s = host + "/" + strings.TrimPrefix(path, "/")
		}
	}
	if at := strings.Index(s, "@"); at >= 0 && !strings.Contains(s[:at], "/") {
		s = s[at+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	return strings.ToLower(s)
}

func gitTop() (string, error) {
	out, err := exec.Command("git", "-c", "safe.directory=*", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRemotes(root string) ([]string, error) {
	out, err := gitCmd(root, "remote").Output()
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, name := range strings.Fields(string(out)) {
		u, uErr := gitCmd(root, "remote", "get-url", name).Output()
		if uErr != nil {
			return nil, uErr
		}
		urls = append(urls, strings.TrimSpace(string(u)))
	}
	return urls, nil
}

func gitList(root, ref string) ([]string, error) {
	out, err := gitCmd(root, "ls-tree", "-r", "--name-only", ref).Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, filepath.ToSlash(line))
		}
	}
	return names, nil
}

func gitShow(root, ref, rel string) ([]byte, error) {
	cmd := gitCmd(root, "show", ref+":"+rel)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			msg := string(exit.Stderr)
			if strings.Contains(msg, "does not exist") || strings.Contains(msg, "exists on disk, but not") {
				return nil, os.ErrNotExist
			}
		}
		return nil, err
	}
	return out, nil
}

func gitOut(root string, args ...string) (string, error) {
	out, err := gitCmd(root, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func gitCmd(root string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+4)
	full = append(full, "-c", "safe.directory=*", "-C", root)
	full = append(full, args...)
	return exec.Command("git", full...)
}

func archiveInto(tmp, root, ref, rel string) (string, error) {
	dest := filepath.Join(tmp, "git")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	cmd := gitCmd(root, "archive", "--format=tar", ref, rel)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	tar := exec.Command("tar", "-x", "-C", dest)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	tar.Stdin = pipe
	if err := tar.Start(); err != nil {
		return "", err
	}
	if err := cmd.Run(); err != nil {
		_ = tar.Wait()
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "exists on disk, but not") {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("git archive %s %s failed: %w (%s)", ref, rel, err, msg)
	}
	if err := tar.Wait(); err != nil {
		return "", fmt.Errorf("tar extract failed: %w", err)
	}
	return filepath.Join(dest, filepath.FromSlash(rel)), nil
}
