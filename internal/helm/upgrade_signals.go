package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

// AssignmentsYAML turns Helm --set assignments (a.b=value) into a values file.
// The values check can then see keys that arrived as Argo CD helm.parameters
// or as --set, not only keys that arrived as a -f file.
func AssignmentsYAML(assignments []string) (string, error) {
	root := map[string]any{}
	for _, raw := range assignments {
		key, val, ok := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		assignPath(root, strings.Split(key, "."), val)
	}
	if len(root) == 0 {
		return "", nil
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func assignPath(node map[string]any, parts []string, val string) {
	if len(parts) == 0 {
		return
	}
	head := parts[0]
	if head == "" {
		return
	}
	if len(parts) == 1 {
		node[head] = val
		return
	}
	next, _ := node[head].(map[string]any)
	if next == nil {
		next = map[string]any{}
		node[head] = next
	}
	assignPath(next, parts[1:], val)
}

type chartDoc struct {
	Version      string `yaml:"version"`
	AppVersion   string `yaml:"appVersion"`
	Dependencies []struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"dependencies"`
}

// UpgradeSignals names changes a green render can hide.
// Every finding is a warning. None of them is a judgment that the upstream
// project forbids the upgrade.
func UpgradeSignals(baseDir, headDir string, baseObjs, headObjs []k8sschema.ManifestObject) []verdict.Finding {
	var out []verdict.Finding
	baseChart := readChartDoc(baseDir)
	headChart := readChartDoc(headDir)
	out = append(out, versionSignals(baseChart, headChart)...)
	out = append(out, dependencySignals(baseDir, headDir, baseChart, headChart)...)
	out = append(out, imageSignals(baseObjs, headObjs)...)
	out = append(out, operatorSignal(baseDir, headDir, out)...)
	return out
}

func readChartDoc(dir string) chartDoc {
	raw, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return chartDoc{}
	}
	var doc chartDoc
	if yaml.Unmarshal(raw, &doc) != nil {
		return chartDoc{}
	}
	return doc
}

type semver struct {
	major, minor, patch int
	ok                  bool
}

func parseSemver(raw string) semver {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return semver{}
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return semver{}
	}
	pat := 0
	if len(parts) >= 3 {
		if p, err := strconv.Atoi(parts[2]); err == nil {
			pat = p
		}
	}
	return semver{major: maj, minor: min, patch: pat, ok: true}
}

func versionSignals(base, head chartDoc) []verdict.Finding {
	var out []verdict.Finding
	bv, hv := parseSemver(base.Version), parseSemver(head.Version)
	if bv.ok && hv.ok && (hv.major > bv.major || (hv.major == bv.major && hv.minor > bv.minor+1)) {
		msg := fmt.Sprintf("Chart version moves from %s to %s.", strings.TrimSpace(base.Version), strings.TrimSpace(head.Version))
		detail := "Stageproof does not read that project's upgrade notes. A skip of more than one minor version, or a new major version, is named here and does not block."
		if hv.major == bv.major {
			skipped := hv.minor - bv.minor - 1
			msg = fmt.Sprintf("Chart version moves from %s to %s, skipping %s.", strings.TrimSpace(base.Version), strings.TrimSpace(head.Version), plural(skipped, "minor version", "minor versions"))
		} else {
			msg = fmt.Sprintf("Chart version moves from %s to %s, a new major version.", strings.TrimSpace(base.Version), strings.TrimSpace(head.Version))
		}
		out = append(out, verdict.Finding{
			RuleID:   "helm.release.version_span",
			Severity: verdict.SeverityWarning,
			Message:  msg,
			Detail:   detail,
		})
	}
	bApp := strings.TrimSpace(base.AppVersion)
	hApp := strings.TrimSpace(head.AppVersion)
	if bApp != "" && hApp != "" && bApp != hApp {
		out = append(out, verdict.Finding{
			RuleID:   "helm.release.app_version_changed",
			Severity: verdict.SeverityWarning,
			Message:  fmt.Sprintf("appVersion moves from %s to %s. The image that the chart runs may change with it.", bApp, hApp),
			Detail:   "This is the chart's declared application version. It is not a test that the registry tag exists.",
		})
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func dependencySignals(baseDir, headDir string, base, head chartDoc) []verdict.Finding {
	old := map[string]string{}
	for _, d := range base.Dependencies {
		if d.Name != "" {
			old[d.Name] = strings.TrimSpace(d.Version)
		}
	}
	var names []string
	for _, d := range head.Dependencies {
		if d.Name == "" {
			continue
		}
		next := strings.TrimSpace(d.Version)
		prev, ok := old[d.Name]
		if !ok || prev == next {
			continue
		}
		if packagedVersion(baseDir, d.Name) == packagedVersion(headDir, d.Name) {
			names = append(names, fmt.Sprintf("%s %s to %s", d.Name, prev, next))
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return []verdict.Finding{{
		RuleID:   "helm.dependency.not_updated",
		Severity: verdict.SeverityWarning,
		Message:  "A Chart.yaml dependency constraint changed, and the packaged chart under charts/ did not: " + strings.Join(names, "; ") + ".",
		Detail:   "helm template renders the chart that is already on disk. Run helm dependency update if the new constraint should be what this check renders.",
	}}
}

func packagedVersion(chartDir, name string) string {
	return strings.TrimSpace(readChartDoc(filepath.Join(chartDir, "charts", name)).Version)
}

type imageRef struct {
	where string
	image string
}

func imageSignals(baseObjs, headObjs []k8sschema.ManifestObject) []verdict.Finding {
	base := indexImages(baseObjs)
	head := indexImages(headObjs)
	if len(base) == 0 || len(head) == 0 {
		return nil
	}
	keys := make([]string, 0, len(head))
	for k := range head {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var msgs []string
	seenMove := map[string]bool{}
	for _, k := range keys {
		old, ok := base[k]
		if !ok || old.image == head[k].image {
			continue
		}
		msg := fmt.Sprintf("%s %s moves from %s to %s.", head[k].where, labelKind(k), old.image, head[k].image)
		if vPrefixOnly(old.image, head[k].image) {
			msg += " The tags differ by a leading v. A registry treats them as different tags."
		}
		move := old.image + " -> " + head[k].image
		if seenMove[move] {
			continue
		}
		seenMove[move] = true
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return nil
	}
	const maxShown = 8
	shown := msgs
	extra := ""
	if len(msgs) > maxShown {
		shown = msgs[:maxShown]
		extra = fmt.Sprintf(" %d more container images also changed.", len(msgs)-maxShown)
	}
	return []verdict.Finding{{
		RuleID:   "helm.image.changed",
		Severity: verdict.SeverityWarning,
		Message:  strings.Join(shown, " ") + extra,
		Detail:   "The render names a different image, including images on custom resources and images passed as container arguments. This check does not ask the registry whether the tag exists.",
	}}
}

// labelKind keeps the sentence readable. The map key ends with /container, /field, or /arg.
func labelKind(key string) string {
	switch {
	case strings.HasSuffix(key, "/container"):
		return "container"
	case strings.HasSuffix(key, "/field"):
		return "field"
	case strings.HasSuffix(key, "/arg"):
		return "argument"
	default:
		return "image"
	}
}

func operatorSignal(baseDir, headDir string, prior []verdict.Finding) []verdict.Finding {
	if !chartShipsCRDs(baseDir) && !chartShipsCRDs(headDir) {
		return nil
	}
	relevant := false
	for _, f := range prior {
		if f.RuleID == "helm.image.changed" || f.RuleID == "helm.release.app_version_changed" {
			relevant = true
			break
		}
	}
	if !relevant {
		return nil
	}
	return []verdict.Finding{{
		RuleID:   "helm.operator.existing_custom_resources",
		Severity: verdict.SeverityWarning,
		Message:  "This chart ships custom resource definitions, and the application it runs is changing. Custom resources that already exist on a real cluster are not in this check.",
		Detail:   "A rehearsal installs the chart into an empty cluster. It does not upgrade data or custom resources that were already there. An empty-cluster result of 0 restarts does not mean an existing database or volume stayed up.",
	}}
}

func chartShipsCRDs(dir string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, "crds"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
			return true
		}
	}
	return false
}

func vPrefixOnly(a, b string) bool {
	at, bt := imageTag(a), imageTag(b)
	if at == "" || bt == "" || at == bt {
		return false
	}
	return at == "v"+bt || bt == "v"+at
}

func imageTag(ref string) string {
	// Drop a digest, then take the tag after the last slash's colon.
	ref = strings.SplitN(ref, "@", 2)[0]
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon < 0 || colon < slash {
		return ""
	}
	return ref[colon+1:]
}

// renderedImageRe finds repo/name:tag refs wherever a chart prints them:
// a container image, a custom resource field, or a container argument.
var renderedImageRe = regexp.MustCompile(`(?:[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?(?::[0-9]+)?/)?[a-z0-9][a-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*:[A-Za-z0-9][A-Za-z0-9._-]{0,127}(?:@sha256:[a-f0-9]{64})?`)

func indexImages(objs []k8sschema.ManifestObject) map[string]imageRef {
	out := map[string]imageRef{}
	for _, obj := range objs {
		walkImages(obj.Spec, obj.ID(), out)
		for i, ref := range renderedImageRe.FindAllString(obj.RawYAML, -1) {
			if !looksLikeImage(ref) {
				continue
			}
			key := obj.ID() + "/raw/" + imageRepo(ref)
			if prev, ok := out[key]; ok && prev.image != ref {
				key = fmt.Sprintf("%s#%d", key, i)
			}
			if _, ok := out[key]; ok {
				continue
			}
			out[key] = imageRef{where: obj.ID() + " " + imageRepo(ref), image: ref}
		}
	}
	return out
}

func imageRepo(ref string) string {
	ref = strings.SplitN(ref, "@", 2)[0]
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon]
	}
	return ref
}

func walkImages(v any, path string, out map[string]imageRef) {
	switch t := v.(type) {
	case map[string]any:
		if img, ok := t["image"].(string); ok && looksLikeImage(img) {
			name, _ := t["name"].(string)
			where := path
			key := path + "/field"
			if name != "" {
				where = path + " " + name
				key = path + "/" + name + "/container"
			}
			out[key] = imageRef{where: where, image: img}
		}
		if args, ok := t["args"].([]any); ok {
			for _, item := range args {
				s, ok := item.(string)
				if !ok {
					continue
				}
				img, flag := imageFromArg(s)
				if img == "" {
					continue
				}
				out[path+"/args/"+flag+"/arg"] = imageRef{where: path + " " + flag, image: img}
			}
		}
		for k, child := range t {
			walkImages(child, path+"."+k, out)
		}
	case []any:
		for i, child := range t {
			walkImages(child, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

func looksLikeImage(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 400 || strings.ContainsAny(s, " \t\n{}") {
		return false
	}
	if strings.Contains(s, "://") {
		return false
	}
	return strings.Contains(s, "/") || strings.Contains(s, ":")
}

func imageFromArg(s string) (img, flag string) {
	eq := strings.LastIndex(s, "=")
	if eq <= 0 {
		return "", ""
	}
	flag = strings.TrimLeft(s[:eq], "-")
	img = strings.TrimSpace(s[eq+1:])
	if flag == "" || !strings.Contains(img, "/") || !looksLikeImage(img) {
		return "", ""
	}
	return img, flag
}
