package impact

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	res, err := runCRDify(crdifyRun, oldCRD, newCRD)
	if err != nil {
		return nil, err
	}
	return crdifyChanges(res, oldCRD, newCRD), nil
}

// runCRDify calls crdify and turns a panic into an error. crdify v0.6.0 is the
// newest release and still dereferences a nil schema. decodeCRD fills that in
// first. This recover is the backstop so one malformed definition cannot crash
// the process. compareCRD reports the error as a warning.
func runCRDify(r *runner.Runner, oldCRD, newCRD *apiextensionsv1.CustomResourceDefinition) (res *runner.Results, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("custom resource schema comparison failed: %v", rec)
		}
	}()
	return r.Run(oldCRD, newCRD), nil
}

// Charts do not carry status.storedVersions. The version marked storage is
// that version. extra is status.storedVersions from a cluster facts file.
func decodeCRD(body map[string]any, extra []string) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode custom resource definition: %w", err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := json.Unmarshal(raw, &crd); err != nil {
		return nil, fmt.Errorf("decode custom resource definition: %w", err)
	}
	// apiextensions v1 drops spec.validation and spec.version. v1beta1 stores
	// the schema there, and a version with a nil Schema makes crdify v0.6.0 panic.
	if err := applyBetaSchema(&crd, body); err != nil {
		return nil, err
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

// applyBetaSchema copies a v1beta1 document onto the v1 struct crdify reads.
// A version that already has an OpenAPI schema is left alone. A document with
// only spec.version becomes one served storage version. A version that still
// has no schema gets an empty one so the comparer walks no fields instead of
// crashing. An empty schema does not invent a field removal when both sides
// are empty.
func applyBetaSchema(crd *apiextensionsv1.CustomResourceDefinition, body map[string]any) error {
	schema, err := betaOpenAPISchema(body)
	if err != nil {
		return err
	}
	if len(crd.Spec.Versions) == 0 {
		if name, ok := stringAt(body, "spec", "version"); ok && name != "" {
			crd.Spec.Versions = []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    name,
				Served:  true,
				Storage: true,
			}}
		}
	}
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Schema != nil && v.Schema.OpenAPIV3Schema != nil {
			continue
		}
		if schema != nil {
			if v.Schema == nil {
				v.Schema = &apiextensionsv1.CustomResourceValidation{}
			}
			v.Schema.OpenAPIV3Schema = schema.DeepCopy()
			continue
		}
		if v.Schema == nil {
			v.Schema = &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{},
			}
		}
	}
	return nil
}

func betaOpenAPISchema(body map[string]any) (*apiextensionsv1.JSONSchemaProps, error) {
	raw, ok := nestedValue(body, "spec", "validation", "openAPIV3Schema")
	if !ok || raw == nil {
		return nil, nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode custom resource schema: %w", err)
	}
	var props apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(buf, &props); err != nil {
		return nil, fmt.Errorf("decode custom resource schema: %w", err)
	}
	return &props, nil
}

func nestedValue(body map[string]any, keys ...string) (any, bool) {
	var cur any = body
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func stringAt(body map[string]any, keys ...string) (string, bool) {
	raw, ok := nestedValue(body, keys...)
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
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

func crdifyChanges(res *runner.Results, oldCRD, newCRD *apiextensionsv1.CustomResourceDefinition) []schemaChange {
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
				ver, prop := versionAndProperty(line)
				out = append(out, schemaChange{Class: "removed", Version: ver, Property: prop, Detail: line})
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
				if c.Name == "enum" && isEnumRelaxation(oldCRD, newCRD, version.Version, prop.Property, text) {
					continue
				}
				out = append(out, schemaChange{
					Class: c.Name, Version: version.Version, Property: prop.Property,
					Detail: text, Values: listFromDetail(text),
				})
			}
		}
	}
	return out
}

// isEnumRelaxation filters false positives from crdify's enum comparator:
//  1. Dropping the enum constraint altogether (old had enum, new has none) widens
//     the schema to accept any value, so no stored object can be rejected.
//  2. Cleaning up an unreachable value from one allOf[i] branch when sibling allOf
//     branches already forbade that value leaves the effective enum unchanged.
func isEnumRelaxation(oldCRD, newCRD *apiextensionsv1.CustomResourceDefinition, ver, propPath, detail string) bool {
	if !strings.Contains(detail, "allowed enum values removed") {
		return false
	}
	if newProp := lookupSchemaProp(newCRD, ver, propPath); newProp != nil && len(newProp.Enum) == 0 {
		return true
	}
	if parentPath, ok := stripAllOfSuffix(propPath); ok {
		oldParent := lookupSchemaProp(oldCRD, ver, parentPath)
		newParent := lookupSchemaProp(newCRD, ver, parentPath)
		if oldParent != nil && newParent != nil {
			oldEff, oldRestricted := effectiveAllOfEnum(oldParent)
			newEff, newRestricted := effectiveAllOfEnum(newParent)
			if !newRestricted {
				return true
			}
			if oldRestricted {
				allKept := true
				for v := range oldEff {
					if !newEff[v] {
						allKept = false
						break
					}
				}
				if allKept {
					return true
				}
			}
		}
	}
	return false
}

func stripAllOfSuffix(path string) (string, bool) {
	i := strings.LastIndex(path, ".allOf[")
	if i < 0 || !strings.HasSuffix(path, "]") {
		return "", false
	}
	idxStr := path[i+len(".allOf[") : len(path)-1]
	if _, err := strconv.Atoi(idxStr); err != nil {
		return "", false
	}
	return path[:i], true
}

func effectiveAllOfEnum(s *apiextensionsv1.JSONSchemaProps) (map[string]bool, bool) {
	var sets []map[string]bool
	if len(s.Enum) > 0 {
		m := make(map[string]bool, len(s.Enum))
		for _, j := range s.Enum {
			m[string(j.Raw)] = true
		}
		sets = append(sets, m)
	}
	for i := range s.AllOf {
		if len(s.AllOf[i].Enum) > 0 {
			m := make(map[string]bool, len(s.AllOf[i].Enum))
			for _, j := range s.AllOf[i].Enum {
				m[string(j.Raw)] = true
			}
			sets = append(sets, m)
		}
	}
	if len(sets) == 0 {
		return nil, false
	}
	inter := make(map[string]bool, len(sets[0]))
	for k := range sets[0] {
		keep := true
		for _, other := range sets[1:] {
			if !other[k] {
				keep = false
				break
			}
		}
		if keep {
			inter[k] = true
		}
	}
	return inter, true
}

func lookupSchemaProp(crd *apiextensionsv1.CustomResourceDefinition, ver, propPath string) *apiextensionsv1.JSONSchemaProps {
	if crd == nil {
		return nil
	}
	var root *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == ver {
			if crd.Spec.Versions[i].Schema != nil {
				root = crd.Spec.Versions[i].Schema.OpenAPIV3Schema
			}
			break
		}
	}
	if root == nil {
		return nil
	}
	p := strings.TrimPrefix(propPath, "^")
	p = strings.TrimPrefix(p, ".")
	if p == "" {
		return root
	}
	cur := root
	for _, seg := range strings.Split(p, ".") {
		if cur == nil || seg == "" {
			return nil
		}
		if seg == "items" {
			if cur.Items == nil {
				return nil
			}
			cur = cur.Items.Schema
			continue
		}
		if seg == "additionalProperties" {
			if cur.AdditionalProperties == nil {
				return nil
			}
			cur = cur.AdditionalProperties.Schema
			continue
		}
		if seg == "not" {
			cur = cur.Not
			continue
		}
		if base, idx, ok := parseIndexedSeg(seg); ok {
			switch base {
			case "allOf":
				if idx < 0 || idx >= len(cur.AllOf) {
					return nil
				}
				cur = &cur.AllOf[idx]
			case "anyOf":
				if idx < 0 || idx >= len(cur.AnyOf) {
					return nil
				}
				cur = &cur.AnyOf[idx]
			case "oneOf":
				if idx < 0 || idx >= len(cur.OneOf) {
					return nil
				}
				cur = &cur.OneOf[idx]
			case "items":
				if cur.Items == nil || idx < 0 || idx >= len(cur.Items.JSONSchemas) {
					return nil
				}
				cur = &cur.Items.JSONSchemas[idx]
			default:
				return nil
			}
			continue
		}
		next, ok := cur.Properties[seg]
		if !ok {
			return nil
		}
		cur = &next
	}
	return cur
}

func parseIndexedSeg(seg string) (string, int, bool) {
	i := strings.IndexByte(seg, '[')
	if i <= 0 || !strings.HasSuffix(seg, "]") {
		return "", 0, false
	}
	idx, err := strconv.Atoi(seg[i+1 : len(seg)-1])
	if err != nil {
		return "", 0, false
	}
	return seg[:i], idx, true
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

func versionAndProperty(line string) (string, string) {
	i := strings.Index(line, "^")
	if i < 0 {
		return "", ""
	}
	property := line[i:]
	head := strings.TrimSuffix(strings.TrimSpace(line[:i]), ".")
	version := head
	if j := strings.LastIndex(head, " "); j >= 0 {
		version = head[j+1:]
	}
	return version, property
}

func listFromDetail(detail string) []string {
	i := strings.Index(detail, "[")
	j := strings.LastIndex(detail, "]")
	if i < 0 || j <= i {
		return nil
	}
	inner := strings.TrimSpace(detail[i+1 : j])
	if inner == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Fields(inner) {
		out = append(out, unquoteJSON(item))
	}
	return out
}

func unquoteJSON(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	return s
}
