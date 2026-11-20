package knowledge

import (
	"embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stageproof/stageproof/internal/verdict"
)

//go:embed operators.yaml crds.yaml
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
