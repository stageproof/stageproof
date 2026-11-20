package impact

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sschema"
)

// obj is one Kubernetes object keyed by group, kind, namespace, and name.
type obj struct {
	Key         string
	ID          string
	Group       string
	Kind        string
	Namespace   string
	Name        string
	Annotations map[string]string
	Body        map[string]any
}

func fromManifest(m k8sschema.ManifestObject) obj {
	ns := m.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}
	body := m.Object
	if body == nil {
		body = map[string]any{}
	}
	return obj{
		Key:         m.Group() + "\x00" + m.Kind + "\x00" + ns + "\x00" + m.Metadata.Name,
		ID:          m.ID(),
		Group:       m.Group(),
		Kind:        m.Kind,
		Namespace:   ns,
		Name:        m.Metadata.Name,
		Annotations: m.Metadata.Annotations,
		Body:        body,
	}
}

func fromJSON(raw []byte) (obj, bool) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return obj{}, false
	}
	md, _ := body["metadata"].(map[string]any)
	if md == nil {
		return obj{}, false
	}
	name, _ := md["name"].(string)
	kind, _ := body["kind"].(string)
	if name == "" || kind == "" {
		return obj{}, false
	}
	ns, _ := md["namespace"].(string)
	if ns == "" {
		ns = "default"
	}
	group := ""
	api, _ := body["apiVersion"].(string)
	if i := strings.Index(api, "/"); i >= 0 {
		group = api[:i]
	}
	ann := map[string]string{}
	if rawAnn, ok := md["annotations"].(map[string]any); ok {
		for k, v := range rawAnn {
			if s, ok := v.(string); ok {
				ann[k] = s
			}
		}
	}
	return obj{
		Key:         group + "\x00" + kind + "\x00" + ns + "\x00" + name,
		ID:          fmt.Sprintf("%s/%s/%s", kind, ns, name),
		Group:       group,
		Kind:        kind,
		Namespace:   ns,
		Name:        name,
		Annotations: ann,
		Body:        body,
	}, true
}

func indexManifests(in []k8sschema.ManifestObject) map[string]obj {
	out := make(map[string]obj, len(in))
	for _, m := range in {
		if m.Kind == "" || m.Metadata.Name == "" {
			continue
		}
		o := fromManifest(m)
		out[o.Key] = o
	}
	return out
}

func kept(o obj) bool {
	return o.Annotations["helm.sh/resource-policy"] == "keep"
}

func specOf(body map[string]any) map[string]any {
	s, _ := body["spec"].(map[string]any)
	if s == nil {
		return map[string]any{}
	}
	return s
}

func canonical(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func cloneAny(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok && m != nil
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func sameLeaf(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ab, aok := a.(bool); aok {
		bb, bok := b.(bool)
		return bok && ab == bb
	}
	if as, aok := a.(string); aok {
		bs, bok := b.(string)
		return bok && as == bs
	}
	if ai, aok := asInt(a); aok {
		if bi, bok := asInt(b); bok {
			return ai == bi
		}
	}
	return canonical(a) == canonical(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// signature ignores the object name and the chart label Helm rewrites every
// release, so a rename of an otherwise identical object can be recognised.
func signature(o obj) string {
	cloned, ok := cloneAny(o.Body).(map[string]any)
	if !ok {
		return ""
	}
	delete(cloned, "status")
	md, _ := cloned["metadata"].(map[string]any)
	if md != nil {
		delete(md, "name")
		delete(md, "uid")
		delete(md, "resourceVersion")
		delete(md, "generation")
		delete(md, "creationTimestamp")
		delete(md, "managedFields")
		dropChartLabel(md)
	}
	spec, _ := cloned["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	tmd, _ := tmpl["metadata"].(map[string]any)
	dropChartLabel(tmd)
	return canonical(cloned)
}

func dropChartLabel(md map[string]any) {
	if md == nil {
		return
	}
	if labels, ok := md["labels"].(map[string]any); ok {
		delete(labels, "helm.sh/chart")
	}
}

func namedList(path string) bool {
	switch path[strings.LastIndex(path, ".")+1:] {
	case "containers", "initContainers", "ephemeralContainers", "env", "ports", "volumes", "volumeMounts":
		return true
	default:
		return false
	}
}

// firstDiff returns the path of the first difference. Lists of containers,
// env, ports, and volumes are matched by name. Other lists keep their order.
func firstDiff(a, b any, path string) string {
	if namedList(path) {
		return diffNamed(a, b, path)
	}
	am, aok := asMap(a)
	bm, bok := asMap(b)
	if aok || bok {
		if !aok || !bok {
			return path
		}
		seen := map[string]bool{}
		var keys []string
		for _, k := range sortedKeys(am) {
			keys = append(keys, k)
			seen[k] = true
		}
		for _, k := range sortedKeys(bm) {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if d := firstDiff(am[k], bm[k], path+"."+k); d != "" {
				return d
			}
		}
		return ""
	}
	as, aok := asSlice(a)
	bs, bok := asSlice(b)
	if aok || bok {
		if !aok || !bok || len(as) != len(bs) {
			return path
		}
		for i := range as {
			if d := firstDiff(as[i], bs[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	}
	if !sameLeaf(a, b) {
		return path
	}
	return ""
}

func diffNamed(a, b any, path string) string {
	as, aok := asSlice(a)
	bs, bok := asSlice(b)
	if !aok || !bok {
		if a == nil && b == nil {
			return ""
		}
		return path
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
	var ordered []string
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	for _, n := range ordered {
		av, aok := am[n]
		bv, bok := bm[n]
		loc := path + "[name=" + n + "]"
		if !aok || !bok {
			return loc
		}
		if d := firstDiff(av, bv, loc); d != "" {
			return d
		}
	}
	return ""
}

func byName(items []any) map[string]any {
	out := map[string]any{}
	for i, item := range items {
		m, ok := asMap(item)
		if !ok {
			out[fmt.Sprintf("%d", i)] = item
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			if p, ok := m["containerPort"]; ok {
				name = fmt.Sprint(p)
			} else {
				name = fmt.Sprintf("%d", i)
			}
		}
		out[name] = m
	}
	return out
}
