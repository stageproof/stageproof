package knowledge

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stageproof/stageproof/internal/verdict"
)

//go:embed operators.yaml crds.yaml served_apis.yaml
var files embed.FS

type OperatorNote struct {
	CRDGroup string `yaml:"crdGroup"`
	Note     string `yaml:"note"`
	Source   string `yaml:"source"`
	Alias    string `yaml:"alias,omitempty"`
}

type CRDBundle struct {
	GroupKind string `yaml:"groupKind"`
	URL       string `yaml:"url"`
	Source    string `yaml:"source"`
}

// ServedAPISet is the discovery list for one Kubernetes minor, captured from
// the published envtest kube-apiserver for that minor.
type ServedAPISet struct {
	Minor  string   `yaml:"minor"`
	Patch  string   `yaml:"patch"`
	Source string   `yaml:"source"`
	APIs   []string `yaml:"apis"`
}

func ServedAPISets() ([]ServedAPISet, error) {
	var out []ServedAPISet
	if err := read("served_apis.yaml", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ExactServedAPIs returns the stored discovery list for the exact minor of
// k8sVersion (for example "1.35" or "v1.35.0").
func ExactServedAPIs(k8sVersion string) ([]string, bool) {
	minor, ok := normalizeMinor(k8sVersion)
	if !ok {
		return nil, false
	}
	sets, err := ServedAPISets()
	if err != nil {
		return nil, false
	}
	for _, set := range sets {
		if set.Minor == minor {
			out := make([]string, len(set.APIs))
			copy(out, set.APIs)
			return out, true
		}
	}
	return nil, false
}

// ServedAPIs returns the stored discovery list for k8sVersion when the local
// API server is not running. For Kubernetes 1.25+ minors outside the stored
// window it uses the nearest stored minor so capability-gated v1 templates
// still select the served API.
func ServedAPIs(k8sVersion string) []string {
	if apis, ok := ExactServedAPIs(k8sVersion); ok {
		return apis
	}
	maj, min, ok := parseMajorMinor(k8sVersion)
	if !ok || maj != 1 || min < 25 {
		return nil
	}
	sets, err := ServedAPISets()
	if err != nil || len(sets) == 0 {
		return nil
	}
	bestIdx := -1
	bestDist := 1 << 30
	for i, set := range sets {
		smaj, smin, sok := parseMajorMinor(set.Minor)
		if !sok || smaj != 1 {
			continue
		}
		dist := min - smin
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil
	}
	out := make([]string, len(sets[bestIdx].APIs))
	copy(out, sets[bestIdx].APIs)
	return out
}

func normalizeMinor(k8sVersion string) (string, bool) {
	maj, min, ok := parseMajorMinor(k8sVersion)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d.%d", maj, min), true
}

func parseMajorMinor(k8sVersion string) (int, int, bool) {
	v := strings.TrimPrefix(strings.TrimSpace(k8sVersion), "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minStr := parts[1]
	for i := 0; i < len(minStr); i++ {
		if minStr[i] < '0' || minStr[i] > '9' {
			minStr = minStr[:i]
			break
		}
	}
	min, err := strconv.Atoi(minStr)
	if err != nil {
		return 0, 0, false
	}
	return maj, min, true
}

func Operators() ([]OperatorNote, error) {
	var out []OperatorNote
	if err := read("operators.yaml", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func CRDBundles() (map[string]string, error) {
	var list []CRDBundle
	if err := read("crds.yaml", &list); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, item := range list {
		out[item.GroupKind] = item.URL
	}
	return out, nil
}

// ValidateSources fails when a knowledge entry has no https source.
func ValidateSources() error {
	ops, err := Operators()
	if err != nil {
		return err
	}
	for _, op := range ops {
		if !strings.HasPrefix(op.Source, "https://") || strings.TrimSpace(op.Note) == "" || op.CRDGroup == "" {
			return fmt.Errorf("operator entry %q needs an https source, a group, and a note", op.CRDGroup)
		}
	}
	var crds []CRDBundle
	if err := read("crds.yaml", &crds); err != nil {
		return err
	}
	for _, item := range crds {
		if !strings.HasPrefix(item.Source, "https://") || !strings.HasPrefix(item.URL, "https://") || item.GroupKind == "" {
			return fmt.Errorf("CRD entry %q needs an https source and url", item.GroupKind)
		}
	}
	sets, err := ServedAPISets()
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return fmt.Errorf("served_apis.yaml has no entries")
	}
	seenMinor := map[string]bool{}
	for _, set := range sets {
		if !strings.HasPrefix(set.Source, "https://") || set.Minor == "" || set.Patch == "" || len(set.APIs) == 0 {
			return fmt.Errorf("served API entry %q needs an https source, minor, patch, and apis", set.Minor)
		}
		if seenMinor[set.Minor] {
			return fmt.Errorf("duplicate served API minor %q", set.Minor)
		}
		seenMinor[set.Minor] = true
		if !sort.StringsAreSorted(set.APIs) {
			return fmt.Errorf("served API list for %s is not sorted", set.Minor)
		}
		for i := 1; i < len(set.APIs); i++ {
			if set.APIs[i] == set.APIs[i-1] {
				return fmt.Errorf("served API list for %s has duplicate %q", set.Minor, set.APIs[i])
			}
		}
	}
	return nil
}

// Findings returns the documented note, and its alias, when the chart text
// mentions the CRD group and the database image refs did not change.
func Findings(chart string, imageChanged bool, workloadRefsUnchanged bool) []verdict.Finding {
	if !imageChanged || !workloadRefsUnchanged {
		return nil
	}
	ops, err := Operators()
	if err != nil {
		return nil
	}
	var out []verdict.Finding
	for _, op := range ops {
		if !strings.Contains(chart, op.CRDGroup) {
			continue
		}
		out = append(out, verdict.Finding{
			RuleID:   "helm.operator.documented_upgrade_behavior",
			Severity: verdict.SeverityWarning,
			Message:  op.Note,
			Detail:   "Source: " + op.Source,
		})
		if op.Alias != "" {
			out = append(out, verdict.Finding{
				RuleID:   op.Alias,
				Severity: verdict.SeverityWarning,
				Message:  op.Note,
				Detail:   "Source: " + op.Source,
			})
		}
	}
	return out
}

func read(name string, dest any) error {
	raw, err := files.ReadFile(name)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(raw, dest)
}
