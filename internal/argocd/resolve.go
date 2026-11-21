package argocd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
)

// MaterializeOptions says which checkout a Git path or a $ref file is read from.
// GitRef empty is the working tree. Remotes, when non-nil, replaces `git remote`.
type MaterializeOptions struct {
	RepoRoot string
	GitRef   string
	Remotes  []string
}

// Materialized is one side of an Application, ready to pass to helm template.
// ValueFiles is already in Argo CD order: valueFiles, then values or
// valuesObject, then parameters. Cleanup removes temporary copies only.
type Materialized struct {
	ChartDir    string
	ValueFiles  []string
	SetValues   []string
	SetStrings  []string
	SetFiles    []string
	ReleaseName string
	Namespace   string
	SkipCrds    *bool
	Labels      map[string]string
	cleanup     func()
}

// Cleanup removes temporary chart unpacks and generated values files.
func (m *Materialized) Cleanup() {
	if m != nil && m.cleanup != nil {
		m.cleanup()
	}
}

// Materialize resolves the chart directory and the values files for spec.
// A Git path or a $ref file in another repository returns NotCheckedError.
// A missing value file returns NotCheckedError when ignoreMissingValueFiles
// is false. The chart is not rendered from its defaults in either case.
func Materialize(ctx context.Context, spec *AppSpec, opt MaterializeOptions) (mat *Materialized, err error) {
	if spec == nil {
		return nil, errors.New("missing application")
	}
	root := opt.RepoRoot
	if root == "" {
		root, err = gitTop()
		if err != nil && (spec.LocalPath != "" || needsRepo(spec)) {
			return nil, err
		}
	}
	remotes := opt.Remotes
	if remotes == nil && root != "" {
		remotes, err = gitRemotes(root)
		if err != nil && spec.LocalPath != "" {
			return nil, err
		}
	}
	if err = preflight(spec, remotes); err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "stageproof-argocd-*")
	if err != nil {
		return nil, err
	}
	mat = &Materialized{
		ReleaseName: spec.ReleaseName,
		Namespace:   spec.Namespace,
		SkipCrds:    spec.SkipCrds,
		Labels:      map[string]string{},
		cleanup:     func() { _ = os.RemoveAll(tmp) },
	}
	defer func() {
		if err != nil {
			mat.Cleanup()
			mat = nil
		}
	}()

	if spec.LocalPath != "" {
		if !sameRepo(spec.RepoURL, remotes) {
			return nil, &NotCheckedError{Reason: fmt.Sprintf("chart repoURL %s is not a remote of this checkout. Stageproof does not clone other Git repositories.", spec.RepoURL)}
		}
		rel, relErr := cleanRel(spec.LocalPath)
		if relErr != nil {
			return nil, &NotCheckedError{Reason: spec.Label + " chart path escapes the repository."}
		}
		if opt.GitRef == "" {
			mat.ChartDir = filepath.Join(root, filepath.FromSlash(rel))
		} else {
			dir, arcErr := archiveInto(tmp, root, opt.GitRef, rel)
			if arcErr != nil {
				return nil, arcErr
			}
			mat.ChartDir = dir
		}
		if _, statErr := os.Stat(filepath.Join(mat.ChartDir, "Chart.yaml")); statErr != nil {
			return nil, &NotCheckedError{Reason: fmt.Sprintf("%s path %s has no Chart.yaml. Stageproof did not render it.", spec.Label, rel)}
		}
	} else {
		dir, pullErr := PullChart(ctx, spec.RepoURL, spec.Chart, spec.TargetRevision, filepath.Join(tmp, "chart"))
		if pullErr != nil {
			return nil, pullErr
		}
		mat.ChartDir = dir
	}

	files, labels, resErr := resolveValueFiles(spec, root, mat.ChartDir, tmp, opt.GitRef, remotes)
	if resErr != nil {
		return nil, resErr
	}
	mat.ValueFiles = append(mat.ValueFiles, files...)
	for k, v := range labels {
		mat.Labels[k] = v
	}
	if strings.TrimSpace(spec.ValuesYAML) != "" {
		name := "helm-values.yaml"
		label := valuesLabel(spec)
		path := filepath.Join(tmp, name)
		if wErr := os.WriteFile(path, []byte(spec.ValuesYAML), 0o644); wErr != nil {
			return nil, wErr
		}
		mat.ValueFiles = append(mat.ValueFiles, path)
		mat.Labels[path] = label
	}
	setFiles, fileErr := resolveFileParams(spec, root, mat.ChartDir, tmp, opt.GitRef, remotes)
	if fileErr != nil {
		return nil, fileErr
	}
	mat.SetFiles = setFiles
	mat.SetValues = append([]string{}, spec.SetParams...)
	mat.SetStrings = append([]string{}, spec.SetStrings...)
	params := append(append([]string{}, spec.SetParams...), spec.SetStrings...)
	if yml, yErr := helm.AssignmentsYAML(params); yErr == nil && strings.TrimSpace(yml) != "" {
		path := filepath.Join(tmp, "helm-parameters.yaml")
		if wErr := os.WriteFile(path, []byte(yml), 0o644); wErr != nil {
			return nil, wErr
		}
		mat.ValueFiles = append(mat.ValueFiles, path)
		mat.Labels[path] = "helm.parameters"
	}
	return mat, nil
}

func needsRepo(spec *AppSpec) bool {
	for _, file := range spec.ValueFiles {
		if strings.HasPrefix(file, "$") {
			return true
		}
	}
	for _, p := range spec.FileParams {
		if strings.HasPrefix(p.Path, "$") {
			return true
		}
	}
	return false
}

func valuesLabel(spec *AppSpec) string {
	kind := "helm.values"
	if spec.ValuesKind == "valuesObject" {
		kind = "helm.valuesObject"
	}
	if spec.Label == "" || strings.HasPrefix(spec.Label, os.TempDir()) {
		return "inline " + kind
	}
	return kind + " in " + spec.Label
}

func preflight(spec *AppSpec, remotes []string) error {
	if spec.LocalPath != "" && !sameRepo(spec.RepoURL, remotes) {
		return &NotCheckedError{Reason: fmt.Sprintf("chart repoURL %s is not a remote of this checkout. Stageproof does not clone other Git repositories.", spec.RepoURL)}
	}
	paths := append([]string{}, spec.ValueFiles...)
	for _, p := range spec.FileParams {
		paths = append(paths, p.Path)
	}
	for _, raw := range paths {
		if strings.Contains(raw, "://") {
			return &NotCheckedError{Reason: "Stageproof does not download " + raw + ". The chart was not rendered from its defaults."}
		}
		if strings.HasPrefix(raw, "$") {
			if _, _, err := locateValue(spec, raw, remotes); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveValueFiles(spec *AppSpec, repoRoot, chartDir, tmp, gitRef string, remotes []string) ([]string, map[string]string, error) {
	type item struct {
		raw  string
		base string // "chart" or "ref:<name>"
		rel  string
		glob bool
	}
	var items []item
	for _, raw := range spec.ValueFiles {
		if strings.Contains(raw, "://") {
			return nil, nil, &NotCheckedError{Reason: "Stageproof does not download a values file from a URL (" + raw + "). The chart was not rendered from its defaults."}
		}
		if strings.Contains(raw, "**") {
			return nil, nil, &NotCheckedError{Reason: "value file " + raw + " uses a recursive glob. Stageproof did not guess which files match, and did not render from chart defaults."}
		}
		base, rel, err := locateValue(spec, raw, remotes)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item{raw: raw, base: base, rel: rel, glob: isGlob(rel)})
	}
	explicit := map[string]bool{}
	for _, it := range items {
		if !it.glob {
			explicit[it.base+"\x00"+cleanSlash(it.rel)] = true
		}
	}
	var out []string
	labels := map[string]string{}
	seen := map[string]bool{}
	add := func(it item, rel, abs string) {
		key := it.base + "\x00" + cleanSlash(rel)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, abs)
		labels[abs] = valueFileLabel(spec, it.raw, rel, gitRef)
	}
	for _, it := range items {
		if it.glob {
			matches, err := globRel(it.base, it.rel, spec, repoRoot, chartDir, gitRef)
			if err != nil {
				return nil, nil, err
			}
			if len(matches) == 0 {
				if spec.IgnoreMissing {
					continue
				}
				return nil, nil, &NotCheckedError{Reason: fmt.Sprintf("value file %s matched nothing and ignoreMissingValueFiles is false. The chart was not rendered from its defaults.", it.raw)}
			}
			for _, rel := range matches {
				if explicit[it.base+"\x00"+cleanSlash(rel)] {
					continue
				}
				abs, err := openRel(it.base, rel, spec, repoRoot, chartDir, tmp, gitRef)
				if err != nil {
					return nil, nil, err
				}
				add(it, rel, abs)
			}
			continue
		}
		abs, err := openRel(it.base, it.rel, spec, repoRoot, chartDir, tmp, gitRef)
		if err != nil {
			if isMissing(err) {
				if spec.IgnoreMissing {
					continue
				}
				return nil, nil, &NotCheckedError{Reason: fmt.Sprintf("value file %s is missing and ignoreMissingValueFiles is false. The chart was not rendered from its defaults.", it.raw)}
			}
			return nil, nil, err
		}
		add(it, it.rel, abs)
	}
	return out, labels, nil
}

func locateValue(spec *AppSpec, raw string, remotes []string) (base, rel string, err error) {
	if strings.HasPrefix(raw, "$") {
		name, rest, ok := splitRef(raw)
		if !ok {
			return "", "", &NotCheckedError{Reason: "value file " + raw + " must start with $<ref>/ and a path inside that source."}
		}
		repoURL, known := spec.Refs[name]
		if !known {
			return "", "", &NotCheckedError{Reason: fmt.Sprintf("value file %s names source ref %s, which is not in this Application. The chart was not rendered from its defaults.", raw, name)}
		}
		if !sameRepo(repoURL, remotes) {
			return "", "", &NotCheckedError{Reason: fmt.Sprintf("value file %s points at %s, which is not a remote of this checkout. Stageproof does not clone other Git repositories, and did not render from chart defaults.", raw, repoURL)}
		}
		rel, err = cleanRel(rest)
		if err != nil {
			return "", "", &NotCheckedError{Reason: "value file " + raw + " escapes the repository."}
		}
		return "ref:" + name, rel, nil
	}
	rel, err = cleanRel(raw)
	if err != nil {
		return "", "", &NotCheckedError{Reason: "value file " + raw + " escapes the chart directory."}
	}
	return "chart", rel, nil
}

func splitRef(raw string) (name, rel string, ok bool) {
	body := strings.TrimPrefix(raw, "$")
	name, rel, found := strings.Cut(body, "/")
	if !found || name == "" || rel == "" {
		return "", "", false
	}
	return name, rel, true
}

func globRel(base, pattern string, spec *AppSpec, repoRoot, chartDir, gitRef string) ([]string, error) {
	if base == "chart" || gitRef == "" {
		root := chartDir
		if base != "chart" {
			root = repoRoot
		}
		abs := filepath.Join(root, filepath.FromSlash(pattern))
		matches, err := filepath.Glob(abs)
		if err != nil {
			return nil, err
		}
		var rels []string
		for _, match := range matches {
			if !within(root, match) {
				return nil, &NotCheckedError{Reason: "value file " + pattern + " escapes its source."}
			}
			rel, err := filepath.Rel(root, match)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			rels = append(rels, filepath.ToSlash(rel))
		}
		sort.Strings(rels)
		return rels, nil
	}
	listed, err := gitList(repoRoot, gitRef)
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, name := range listed {
		if globMatch(pattern, name) {
			rels = append(rels, name)
		}
	}
	sort.Strings(rels)
	return rels, nil
}

func openRel(base, rel string, spec *AppSpec, repoRoot, chartDir, tmp, gitRef string) (string, error) {
	if base == "chart" || gitRef == "" {
		root := chartDir
		if base != "chart" {
			root = repoRoot
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if !within(root, abs) {
			return "", &NotCheckedError{Reason: "value file " + rel + " escapes its source."}
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("value file %s is a directory", rel)
		}
		return abs, nil
	}
	return gitMaterialize(tmp, repoRoot, gitRef, rel)
}

func resolveFileParams(spec *AppSpec, repoRoot, chartDir, tmp, gitRef string, remotes []string) ([]string, error) {
	var out []string
	for _, p := range spec.FileParams {
		if strings.Contains(p.Path, "://") {
			return nil, &NotCheckedError{Reason: "Stageproof does not download helm file parameter " + p.Name + " from a URL."}
		}
		if isGlob(p.Path) {
			return nil, &NotCheckedError{Reason: "helm file parameter " + p.Name + " is a glob. Stageproof did not guess the file."}
		}
		base, rel, err := locateValue(spec, p.Path, remotes)
		if err != nil {
			return nil, err
		}
		abs, err := openRel(base, rel, spec, repoRoot, chartDir, tmp, gitRef)
		if err != nil {
			if isMissing(err) {
				return nil, &NotCheckedError{Reason: fmt.Sprintf("helm file parameter %s (%s) is missing. The chart was not rendered from its defaults.", p.Name, p.Path)}
			}
			return nil, err
		}
		out = append(out, p.Name+"="+abs)
	}
	return out, nil
}

func valueFileLabel(spec *AppSpec, raw, rel, gitRef string) string {
	shown := raw
	if gitRef != "" {
		shown = gitRef + ":" + rel
	}
	if spec.Label == "" {
		return shown
	}
	return shown
}

func isGlob(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func globMatch(pattern, name string) bool {
	pp := strings.Split(filepath.ToSlash(pattern), "/")
	nn := strings.Split(filepath.ToSlash(name), "/")
	if len(pp) != len(nn) {
		return false
	}
	for i := range pp {
		ok, err := filepath.Match(pp[i], nn[i])
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func cleanRel(rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return "", errors.New("empty path")
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", errors.New("path escapes")
	}
	return rel, nil
}

func cleanSlash(rel string) string {
	got, err := cleanRel(rel)
	if err != nil {
		return filepath.ToSlash(rel)
	}
	return got
}

func within(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist)
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

func gitCmd(root string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+4)
	full = append(full, "-c", "safe.directory=*", "-C", root)
	full = append(full, args...)
	return exec.Command("git", full...)
}

func archiveInto(tmp, root, ref, rel string) (string, error) {
	cmd := gitCmd(root, "archive", "--format=tar", ref, rel)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	tar := exec.Command("tar", "-x", "-C", tmp)
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
		return "", fmt.Errorf("git archive %s %s failed: %w (%s)", ref, rel, err, strings.TrimSpace(stderr.String()))
	}
	if err := tar.Wait(); err != nil {
		return "", fmt.Errorf("tar extract failed: %w", err)
	}
	return filepath.Join(tmp, filepath.FromSlash(rel)), nil
}

func gitMaterialize(tmp, root, ref, rel string) (string, error) {
	cmd := gitCmd(root, "show", ref+":"+filepath.ToSlash(rel))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "exists on disk, but not") {
			return "", os.ErrNotExist
		}
		return "", fmt.Errorf("git show %s:%s failed: %w (%s)", ref, rel, err, strings.TrimSpace(msg))
	}
	dest := filepath.Join(tmp, "rev", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		return "", err
	}
	return dest, nil
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

// ApplicationsReferencing returns Application files whose chart path or value
// files include one of changed. changed entries may name files that no longer
// exist. The walk does not clone anything.
func ApplicationsReferencing(repoRoot string, changed []string, ph map[string]string) []string {
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
	var found []string
	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml.tmpl") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || info.Size() > 2<<20 {
			return nil
		}
		spec, _, parseErr := ParseApplicationFile(path, ph)
		if parseErr != nil || spec == nil {
			return nil
		}
		if specMentions(spec, want) {
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				rel = path
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(found)
	return found
}

func relChanged(root, path string) string {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func specMentions(spec *AppSpec, changed map[string]bool) bool {
	if spec.LocalPath != "" {
		prefix, err := cleanRel(spec.LocalPath)
		if err == nil {
			for path := range changed {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					return true
				}
			}
		}
	}
	for _, raw := range spec.ValueFiles {
		if fileMentions(spec, raw, changed) {
			return true
		}
	}
	for _, p := range spec.FileParams {
		if fileMentions(spec, p.Path, changed) {
			return true
		}
	}
	return false
}

func fileMentions(spec *AppSpec, raw string, changed map[string]bool) bool {
	var rel string
	if strings.HasPrefix(raw, "$") {
		_, rest, ok := splitRef(raw)
		if !ok {
			return false
		}
		got, err := cleanRel(rest)
		if err != nil {
			return false
		}
		rel = got
	} else if spec.LocalPath != "" {
		base, err := cleanRel(spec.LocalPath)
		if err != nil {
			return false
		}
		child, err := cleanRel(raw)
		if err != nil {
			return false
		}
		rel = cleanSlash(base + "/" + child)
	} else {
		return false
	}
	if isGlob(rel) {
		dir := globDir(rel)
		for path := range changed {
			if dir == "" || path == dir || strings.HasPrefix(path, dir+"/") || globMatch(rel, path) {
				if globMatch(rel, path) || (dir != "" && strings.HasPrefix(path, dir+"/")) {
					return true
				}
			}
		}
		return false
	}
	return changed[rel]
}

func globDir(pattern string) string {
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	var keep []string
	for _, part := range parts {
		if isGlob(part) {
			break
		}
		keep = append(keep, part)
	}
	return strings.Join(keep, "/")
}
