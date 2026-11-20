package impact

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/crdify/pkg/config"
	"sigs.k8s.io/crdify/pkg/runner"
)

// crdifySchema is the one place that imports sigs.k8s.io/crdify.
// The module is pinned to v0.6.0, which is still pre-1.0.
type crdifySchema struct{}

var (
	crdifyOnce sync.Once
	crdifyRun  *runner.Runner
	crdifyErr  error
)

func (crdifySchema) Compare(old, new map[string]any, stored []string) ([]schemaChange, error) {
	crdifyOnce.Do(func() {
		cfg := crdifyConfig()
		if err := config.ValidateConfig(cfg); err != nil {
			crdifyErr = err
			return
		}
		crdifyRun, crdifyErr = runner.New(cfg, runner.DefaultRegistry())
	})
	if crdifyErr != nil {
		return nil, crdifyErr
	}
	oldCRD, err := decodeCRD(old, stored)
	if err != nil {
		return nil, err
	}
	newCRD, err := decodeCRD(new, nil)
	if err != nil {
		return nil, err
	}
	res := crdifyRun.Run(oldCRD, newCRD)
	return crdifyChanges(res), nil
}

// Charts do not carry status.storedVersions. The version marked storage is
// that version. Cluster facts, when a later task has them, are added here.
func decodeCRD(body map[string]any, extra []string) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode custom resource definition: %w", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := json.Unmarshal(raw, &crd); err != nil {
		return nil, fmt.Errorf("decode custom resource definition: %w", err)
	}
	if len(crd.Status.StoredVersions) == 0 {
		for _, v := range crd.Spec.Versions {
			if v.Storage {
				crd.Status.StoredVersions = append(crd.Status.StoredVersions, v.Name)
			}
		}
	}
	have := map[string]bool{}
	for _, v := range crd.Status.StoredVersions {
		have[v] = true
	}
	for _, v := range extra {
		if v != "" && !have[v] {
			crd.Status.StoredVersions = append(crd.Status.StoredVersions, v)
			have[v] = true
		}
	}
	return &crd, nil
}

// Only the incompatibilities task 1.5 lists are enabled. Description edits,
// new optional fields, and other schema noise stay off. Unhandled diffs are
// ignored so a CRD change is not a block by default.
func crdifyConfig() *config.Config {
	want := map[string]config.EnforcementPolicy{
		"scope":                config.EnforcementPolicyError,
		"storedVersionRemoval": config.EnforcementPolicyError,
		"existingFieldRemoval": config.EnforcementPolicyWarn,
		"type":                 config.EnforcementPolicyWarn,
		"required":             config.EnforcementPolicyWarn,
		"enum":                 config.EnforcementPolicyWarn,
	}
	names := runner.DefaultRegistry().Registered()
	sort.Strings(names)
	cfg := &config.Config{UnhandledEnforcement: config.EnforcementPolicyNone}
	for _, name := range names {
		pol := config.EnforcementPolicyNone
		if p, ok := want[name]; ok {
			pol = p
		}
		vc := config.ValidationConfig{Name: name, Enforcement: pol}
		if name == "enum" {
			// Adding an allowed value is not narrowing.
			vc.Configuration = map[string]any{"additionPolicy": "Allow"}
		}
		cfg.Validations = append(cfg.Validations, vc)
	}
	return cfg
}

func crdifyChanges(res *runner.Results) []schemaChange {
	if res == nil {
		return nil
	}
	var out []schemaChange
	for _, c := range res.CRDValidation {
		text := firstText(c.Errors, c.Warnings)
		if text == "" {
			continue
		}
		switch c.Name {
		case "scope":
			out = append(out, schemaChange{Class: "scope", Detail: text})
		case "storedVersionRemoval":
			out = append(out, schemaChange{Class: "stored", Detail: text})
		case "existingFieldRemoval":
			for _, line := range append(append([]string{}, c.Errors...), c.Warnings...) {
				out = append(out, schemaChange{Class: "removed", Property: propertyFrom(line), Detail: line})
			}
		}
	}
	for _, version := range res.SameVersionValidation {
		for _, prop := range version.PropertyComparisons {
			for _, c := range prop.ComparisonResults {
				if c.Name != "type" && c.Name != "required" && c.Name != "enum" {
					continue
				}
				text := firstText(c.Errors, c.Warnings)
				if text == "" {
					continue
				}
				out = append(out, schemaChange{Class: c.Name, Property: prop.Property, Detail: text})
			}
		}
	}
	return out
}

func firstText(errors, warnings []string) string {
	if len(errors) > 0 {
		return errors[0]
	}
	if len(warnings) > 0 {
		return warnings[0]
	}
	return ""
}

func propertyFrom(line string) string {
	if i := strings.Index(line, "^"); i >= 0 {
		return line[i:]
	}
	return ""
}
