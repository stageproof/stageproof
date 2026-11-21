// Package k8sdiff compares parsed Kubernetes objects.
// Named lists match the same way as internal/impact: containers, env, ports,
// volumes, and volume mounts are paired by name, so a reorder is not a change.
package k8sdiff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sschema"
)

// Effect is one field that differs. Value is the canonical form of the new
// side. Callers compare effects and must not print Value.
type Effect struct {
	Object string
	Path   string
	Value  string
}

// Diff returns the sorted field changes from a to b.
// mask keys are object + "\x00" + path. A masked path and everything under it
// is ignored. That is how a field that changes between two identical renders
// (a random token, or a checksum of one) stays out of later comparisons.
func Diff(a, b []k8sschema.ManifestObject, mask map[string]bool) []Effect {
	am := index(a)
	bm := index(b)
	keys := make([]string, 0, len(am)+len(bm))
	seen := map[string]bool{}
	for k := range am {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range bm {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Effect
	for _, k := range keys {
		ao, aok := am[k]
		bo, bok := bm[k]
		if !aok || !bok {
			out = append(out, Effect{Object: publicName(k), Path: ""})
			continue
		}
		walk(ao, bo, k, "", mask, &out)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Object != out[j].Object {
			return out[i].Object < out[j].Object
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// Same reports whether the two object sets match after the mask is applied.
func Same(a, b []k8sschema.ManifestObject, mask map[string]bool) bool {
	return len(Diff(a, b, mask)) == 0
}

// MaskOf is every path that differs between two renders of the same inputs.
func MaskOf(a, b []k8sschema.ManifestObject) map[string]bool {
	mask := map[string]bool{}
	for _, e := range Diff(a, b, nil) {
		mask[e.Object+"\x00"+e.Path] = true
	}
	return mask
}

func index(objs []k8sschema.ManifestObject) map[string]map[string]any {
	out := map[string]map[string]any{}
	dup := map[string]int{}
	for _, o := range objs {
		key := o.APIVersion + "\x00" + o.ID()
		if _, ok := out[key]; ok {
			dup[key]++
			key = fmt.Sprintf("%s#%d", key, dup[key])
		}
		body, _ := cloneMap(o.Object)
		out[key] = body
	}
	return out
}

func publicName(key string) string {
	if i := strings.IndexByte(key, '\x00'); i >= 0 {
		return key[i+1:]
	}
	return key
}

func walk(a, b any, obj, path string, mask map[string]bool, out *[]Effect) {
	if path != "" && masked(mask, obj, path) {
		return
	}
	if namedList(path) {
		walkNamed(a, b, obj, path, mask, out)
		return
	}
	am, aok := asMap(a)
	bm, bok := asMap(b)
	if aok || bok {
		if !aok || !bok {
			emit(out, obj, path, b)
			return
		}
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		ordered := make([]string, 0, len(keys))
		for k := range keys {
			ordered = append(ordered, k)
		}
		sort.Strings(ordered)
		for _, k := range ordered {
			next := k
			if path != "" {
				next = path + "." + k
			}
			walk(am[k], bm[k], obj, next, mask, out)
		}
		return
	}
	as, aok := asSlice(a)
	bs, bok := asSlice(b)
	if aok || bok {
		if !aok || !bok || len(as) != len(bs) {
			emit(out, obj, path, b)
			return
		}
		for i := range as {
			walk(as[i], bs[i], obj, fmt.Sprintf("%s[%d]", path, i), mask, out)
		}
		return
	}
	if canon(a) != canon(b) {
		emit(out, obj, path, b)
	}
}

func walkNamed(a, b any, obj, path string, mask map[string]bool, out *[]Effect) {
	as, aok := asSlice(a)
	bs, bok := asSlice(b)
	if !aok || !bok {
		if canon(a) != canon(b) {
			emit(out, obj, path, b)
		}
		return
	}
	am := byName(as)
	bm := byName(bs)
	names := map[string]bool{}
	for n := range am {
		names[n] = true
	}
	for n := range bm {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	for _, n := range ordered {
		loc := path + "[name=" + n + "]"
		av, aHas := am[n]
		bv, bHas := bm[n]
		if !aHas || !bHas {
			emit(out, obj, loc, bv)
			continue
		}
		walk(av, bv, obj, loc, mask, out)
	}
}

func emit(out *[]Effect, obj, path string, value any) {
	*out = append(*out, Effect{Object: publicName(obj), Path: path, Value: canon(value)})
}

func masked(mask map[string]bool, obj, path string) bool {
	if mask[obj+"\x00"+path] || mask[publicName(obj)+"\x00"+path] {
		return true
	}
	// A parent that itself differed between identical renders covers children.
	for p := path; ; {
		i := strings.LastIndex(p, ".")
		j := strings.LastIndex(p, "[")
		cut := i
		if j > cut {
			cut = j
		}
		if cut <= 0 {
			return false
		}
		p = p[:cut]
		if mask[obj+"\x00"+p] || mask[publicName(obj)+"\x00"+p] {
			return true
		}
	}
}

func namedList(path string) bool {
	if path == "" {
		return false
	}
	last := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		last = path[i+1:]
	}
	if i := strings.IndexByte(last, '['); i >= 0 {
		last = last[:i]
	}
	switch last {
	case "containers", "initContainers", "ephemeralContainers", "env", "ports", "volumes", "volumeMounts":
		return true
	default:
		return false
	}
}

func byName(items []any) map[string]any {
	out := map[string]any{}
	for i, item := range items {
		name := fmt.Sprintf("#%d", i)
		if m, ok := asMap(item); ok {
			if n, ok := m["name"].(string); ok && n != "" {
				name = n
			}
		}
		out[name] = item
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func canon(v any) string {
	raw, err := json.Marshal(norm(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func norm(v any) any {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t.String()
		}
		return f
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = norm(child)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = child
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = norm(child)
		}
		return out
	default:
		return v
	}
}

func cloneMap(in map[string]any) (map[string]any, bool) {
	if in == nil {
		return map[string]any{}, true
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}
