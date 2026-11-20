package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// schemaChange is one incompatibility reported by the CRD comparer.
// Class is scope, stored, type, required, enum, or removed.
type schemaChange struct {
	Class    string
	Property string
	Detail   string
}

// schemaDiffer is the only call into the CRD comparison library.
type schemaDiffer interface {
	Compare(old, new map[string]any, stored []string) ([]schemaChange, error)
}

// schemas is the crdify v0.6.0 comparer. Tests may replace it.
var schemas schemaDiffer = crdifySchema{}

func crdSafety(before, after map[string]obj, opt Options) []verdict.Finding {
	var findings []verdict.Finding
	var users []obj
	for _, o := range before {
		if o.Kind != "CustomResourceDefinition" {
			users = append(users, o)
		}
	}
	for k, neu := range after {
		if neu.Kind != "CustomResourceDefinition" {
			continue
		}
		old, ok := before[k]
		if !ok {
			continue
		}
		findings = append(findings, compareCRD(old, neu, users, opt.storedFor(neu.Name))...)
	}
	return findings
}

func compareCRD(old, neu obj, users []obj, stored []string) []verdict.Finding {
	changes, err := schemas.Compare(old.Body, neu.Body, stored)
	if err != nil {
		return []verdict.Finding{{
			RuleID:   "crd.schema.incompatible",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  "The custom resource definition could not be compared.",
			Detail:   err.Error(),
		}}
	}
	var findings []verdict.Finding
	var unused []string
	storedHit := false
	for _, c := range changes {
		switch c.Class {
		case "scope":
			findings = append(findings, verdict.Finding{
				RuleID:   "crd.scope_changed",
				Severity: verdict.SeverityBlocker,
				Resource: neu.ID,
				Message:  fmt.Sprintf("%s changed scope from %s to %s. Existing objects are not valid for the new scope.", neu.ID, scopeOf(old.Body), scopeOf(neu.Body)),
			})
		case "stored":
			storedHit = true
			findings = append(findings, verdict.Finding{
				RuleID:   "crd.existing_object_invalid",
				Severity: verdict.SeverityBlocker,
				Resource: neu.ID,
				Message:  fmt.Sprintf("%s no longer includes a storage version (%s). Objects stored as that version cannot be read.", neu.ID, c.Detail),
				Detail:   "A version marked storage, or a stored version from cluster facts, was removed.",
			})
		default:
			prop := strings.TrimPrefix(c.Property, "^.")
			prop = strings.TrimPrefix(prop, "^")
			if who := usersOf(users, old.Body, c.Property); len(who) > 0 {
				findings = append(findings, verdict.Finding{
					RuleID:   "crd.existing_object_invalid",
					Severity: verdict.SeverityBlocker,
					Resource: who[0].ID,
					Message:  fmt.Sprintf("%s uses %s, and that field changed (%s) on %s. The object does not match the new schema.", who[0].ID, prop, c.Class, neu.Name),
				})
				continue
			}
			unused = append(unused, prop+" ("+c.Class+")")
		}
	}
	if len(unused) > 0 {
		sort.Strings(unused)
		findings = append(findings, verdict.Finding{
			RuleID:   "crd.schema.incompatible",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("Incompatible fields on %s: %s. No object rendered from the current chart uses them.", neu.Name, strings.Join(unused, ", ")),
			Detail:   "A field was removed, its type changed, it became required, or its enum was narrowed. This stays a warning until a rendered object uses the field.",
		})
	}
	findings = append(findings, unserved(old, neu, users, storedHit)...)
	return findings
}

func unserved(old, neu obj, users []obj, storageAlready bool) []verdict.Finding {
	oldServed := served(old.Body)
	newServed := served(neu.Body)
	storage := map[string]bool{}
	for _, v := range storageVersions(old.Body) {
		storage[v] = true
	}
	var findings []verdict.Finding
	seen := map[string]bool{}
	for _, u := range users {
		if !crMatches(u, old.Body) {
			continue
		}
		ver := versionOf(u)
		if !oldServed[ver] || newServed[ver] || seen[u.ID+"/"+ver] {
			continue
		}
		if storageAlready && storage[ver] {
			continue
		}
		seen[u.ID+"/"+ver] = true
		findings = append(findings, verdict.Finding{
			RuleID:   "crd.existing_object_invalid",
			Severity: verdict.SeverityBlocker,
			Resource: u.ID,
			Message:  fmt.Sprintf("%s uses version %s of %s, and that version is no longer served.", u.ID, ver, neu.Name),
		})
	}
	return findings
}

func usersOf(users []obj, crd map[string]any, property string) []obj {
	var out []obj
	for _, u := range users {
		if crMatches(u, crd) && usesProperty(u.Body, property) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func crMatches(o obj, crd map[string]any) bool {
	spec := specOf(crd)
	group, _ := spec["group"].(string)
	names, _ := spec["names"].(map[string]any)
	kind, _ := names["kind"].(string)
	return group != "" && kind != "" && o.Group == group && o.Kind == kind
}

func versionOf(o obj) string {
	api, _ := o.Body["apiVersion"].(string)
	if i := strings.LastIndex(api, "/"); i >= 0 {
		return api[i+1:]
	}
	return api
}

func served(body map[string]any) map[string]bool {
	out := map[string]bool{}
	versions, _ := specOf(body)["versions"].([]any)
	for _, v := range versions {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		on, _ := m["served"].(bool)
		if name != "" && on {
			out[name] = true
		}
	}
	return out
}

func storageVersions(body map[string]any) []string {
	var out []string
	versions, _ := specOf(body)["versions"].([]any)
	for _, v := range versions {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		on, _ := m["storage"].(bool)
		if name != "" && on {
			out = append(out, name)
		}
	}
	return out
}

func scopeOf(body map[string]any) string {
	s, _ := specOf(body)["scope"].(string)
	if s == "" {
		return "unset"
	}
	return s
}

func usesProperty(body map[string]any, property string) bool {
	path := strings.TrimPrefix(property, "^.")
	path = strings.TrimPrefix(path, "^")
	if path == "" {
		return false
	}
	return walkPath(body, splitProp(path))
}

func splitProp(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, ".") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func walkPath(v any, parts []string) bool {
	if len(parts) == 0 {
		return v != nil
	}
	m, ok := asMap(v)
	if !ok {
		return false
	}
	name, indexed := cutIndex(parts[0])
	next, ok := m[name]
	if !ok || next == nil {
		return false
	}
	if indexed {
		arr, ok := asSlice(next)
		if !ok {
			return false
		}
		if len(parts) == 1 {
			return len(arr) > 0
		}
		for _, item := range arr {
			if walkPath(item, parts[1:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 1 {
		return true
	}
	return walkPath(next, parts[1:])
}

func cutIndex(part string) (string, bool) {
	if i := strings.Index(part, "["); i >= 0 {
		return part[:i], true
	}
	return part, false
}
