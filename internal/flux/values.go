package flux

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// piece is one valuesFrom entry after the object was read.
// A target path is applied after spec.values. A root map is applied before it.
type piece struct {
	root    map[string]any
	target  string
	literal bool
	flat    string
}

// Merge applies Flux valuesFrom order, then spec.values, then targetPath entries.
// https://fluxcd.io/flux/components/helm/helmreleases/#values-references
func mergeValues(parts []piece, inline map[string]any) map[string]any {
	out := map[string]any{}
	var targeted []piece
	for _, part := range parts {
		if part.target != "" {
			targeted = append(targeted, part)
			continue
		}
		out = mergeMaps(out, part.root)
	}
	out = mergeMaps(out, inline)
	for _, part := range targeted {
		setPath(out, part.target, part.flat, part.literal)
	}
	return out
}

func mergeMaps(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		child, ok := v.(map[string]any)
		if ok {
			if base, baseOK := dst[k].(map[string]any); baseOK {
				dst[k] = mergeMaps(base, child)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

func setPath(root map[string]any, path, raw string, literal bool) {
	parts := splitSetPath(path)
	if len(parts) == 0 {
		return
	}
	val := interpretSet(raw, literal)
	cur := root
	for i, part := range parts {
		if i == len(parts)-1 {
			cur[part] = val
			return
		}
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
}

func splitSetPath(path string) []string {
	var parts []string
	var b strings.Builder
	escape := false
	for _, r := range path {
		switch {
		case escape:
			b.WriteRune(r)
			escape = false
		case r == '\\':
			escape = true
		case r == '.':
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if escape {
		b.WriteByte('\\')
	}
	parts = append(parts, b.String())
	return parts
}

func interpretSet(raw string, literal bool) any {
	if literal {
		return raw
	}
	switch raw {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if i, err := strconv.Atoi(raw); err == nil {
		return i
	}
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		inner := strings.TrimSuffix(strings.TrimPrefix(raw, "{"), "}")
		var list []any
		for _, part := range strings.Split(inner, ",") {
			list = append(list, interpretSet(strings.TrimSpace(part), false))
		}
		return list
	}
	return raw
}

// readObjectValues reads one ConfigMap or Secret document.
// The returned gap is set when the value must not be used. Secret contents
// are never included in gap text.
func readObjectValues(raw []byte, ref ValueFrom, namespace string) (piece, string, error) {
	key := ref.ValuesKey
	if key == "" {
		key = "values.yaml"
	}
	display := fmt.Sprintf("%s/%s/%s key %s", ref.Kind, namespaceOrDefault(namespace), ref.Name, key)
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
			Data       map[string]string `yaml:"data"`
			StringData map[string]string `yaml:"stringData"`
			Sops       yaml.Node         `yaml:"sops"`
		}
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return piece{}, "", err
		}
		if doc.Kind != ref.Kind || doc.Metadata.Name != ref.Name {
			continue
		}
		if doc.Sops.Kind != 0 || encryptedValues(doc.Data) || encryptedValues(doc.StringData) {
			return piece{}, display + " is encrypted. Stageproof does not decrypt it.", nil
		}
		var payload string
		var ok bool
		if ref.Kind == "Secret" {
			if payload, ok = doc.StringData[key]; !ok {
				encoded, has := doc.Data[key]
				if !has {
					return piece{}, display + " is not in the Secret.", nil
				}
				decoded, decErr := base64.StdEncoding.DecodeString(encoded)
				if decErr != nil {
					return piece{}, display + " could not be read.", nil
				}
				payload = string(decoded)
			}
		} else {
			payload, ok = doc.Data[key]
			if !ok {
				return piece{}, display + " is not in the ConfigMap.", nil
			}
		}
		if ref.TargetPath != "" {
			return piece{target: ref.TargetPath, literal: ref.Literal, flat: payload}, "", nil
		}
		var root map[string]any
		if err := yaml.Unmarshal([]byte(payload), &root); err != nil || root == nil {
			return piece{}, display + " is not a YAML mapping.", nil
		}
		return piece{root: root}, "", nil
	}
	return piece{}, display + " is not in this repository.", nil
}

func encryptedValues(data map[string]string) bool {
	for _, v := range data {
		if strings.Contains(v, "ENC[") {
			return true
		}
	}
	return false
}

func namespaceOrDefault(ns string) string {
	if strings.TrimSpace(ns) == "" {
		return "default"
	}
	return ns
}
