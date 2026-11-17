package helm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template/parse"

	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

const segSep = "\x00"

func joinSegs(segs []string) string {
	return strings.Join(segs, segSep)
}

func splitSegs(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(key, segSep)
}

// FormatKeyPath formats structured YAML segments cleanly, quoting keys that contain dots or slashes
// (e.g. `configs.cm["application.resourceTrackingMethod"]`).
func FormatKeyPath(segs []string) string {
	var b strings.Builder
	for i, s := range segs {
		if strings.ContainsAny(s, "./:") {
			fmt.Fprintf(&b, "[%q]", s)
		} else {
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s)
		}
	}
	return b.String()
}

// ChartUsage captures every `.Values.*` path referenced in a chart's templates and schema,
// wildcard map prefixes (`toYaml`, `omit`, `range`, empty `{}` maps, `additionalProperties`),
// and `required` `.Values.*` paths with no non-empty default.
type ChartUsage struct {
	ExactPaths       map[string]bool
	WildcardPrefixes map[string]bool
	RequiredPaths    map[string]string // canonical key -> human message
	NonEmptyDefaults map[string]bool
	DynamicRoot      bool
}

func NewChartUsage() *ChartUsage {
	return &ChartUsage{
		ExactPaths:       make(map[string]bool),
		WildcardPrefixes: make(map[string]bool),
		RequiredPaths:    make(map[string]string),
		NonEmptyDefaults: make(map[string]bool),
	}
}

func (u *ChartUsage) MarkExactSegs(segs []string) {
	if len(segs) == 0 {
		return
	}
	for i := 1; i <= len(segs); i++ {
		u.ExactPaths[joinSegs(segs[:i])] = true
	}
}

func (u *ChartUsage) MarkWildcardSegs(segs []string) {
	if len(segs) == 0 {
		u.DynamicRoot = true
		return
	}
	u.MarkExactSegs(segs)
	u.WildcardPrefixes[joinSegs(segs)] = true
}

func (u *ChartUsage) ConsumesSegs(segs []string) bool {
	if u.DynamicRoot || len(segs) == 0 {
		return true
	}
	if segs[0] == "global" {
		return true
	}
	key := joinSegs(segs)
	if u.ExactPaths[key] {
		return true
	}
	for i := 1; i <= len(segs); i++ {
		if u.WildcardPrefixes[joinSegs(segs[:i])] {
			return true
		}
	}
	return false
}

// AnalyzeChartUsage parses all Go templates, default values.yaml, values.schema.json,
// and unpacked subcharts (`charts/<subchart>`) inside chartDir.
func AnalyzeChartUsage(chartDir string) (*ChartUsage, error) {
	return analyzeChartUsageWithPrefix(chartDir, nil)
}

func analyzeChartUsageWithPrefix(chartDir string, prefix []string) (*ChartUsage, error) {
	u := NewChartUsage()
	withPfx := func(segs []string) []string {
		if len(prefix) == 0 {
			return segs
		}
		out := make([]string, 0, len(prefix)+len(segs))
		out = append(out, prefix...)
		out = append(out, segs...)
		return out
	}

	// 1. Parse chart's own default `values.yaml` keys + empty-dict wildcard hints + non-empty defaults
	defaultValuesPath := filepath.Join(chartDir, "values.yaml")
	if raw, err := os.ReadFile(defaultValuesPath); err == nil {
		var node yaml.Node
		if yaml.Unmarshal(raw, &node) == nil {
			walkDefaultValuesNode(&node, nil, u, withPfx)
		}
	}

	// 2. Parse templates/*.yaml, *.yml, *.tpl, *.txt, *.json
	tmplDir := filepath.Join(chartDir, "templates")
	if info, err := os.Stat(tmplDir); err == nil && info.IsDir() {
		_ = filepath.WalkDir(tmplDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != ".yaml" && ext != ".yml" && ext != ".tpl" && ext != ".txt" && ext != ".json" {
				return nil
			}
			raw, rErr := os.ReadFile(path)
			if rErr != nil {
				return nil
			}
			extractTemplateValues(string(raw), u, withPfx)
			return nil
		})
	}

	// 3. Parse Chart.yaml dependency `condition` and `alias` keys
	chartYamlPath := filepath.Join(chartDir, "Chart.yaml")
	var depAliases map[string]string
	if raw, err := os.ReadFile(chartYamlPath); err == nil {
		depAliases = extractChartDependencies(raw, u, withPfx)
	}

	// 4. Parse `values.schema.json` if present
	schemaPath := filepath.Join(chartDir, "values.schema.json")
	if raw, err := os.ReadFile(schemaPath); err == nil {
		var schema map[string]any
		if json.Unmarshal(raw, &schema) == nil {
			walkJSONSchema(schema, nil, u, withPfx)
		}
	}

	// 5. Recurse into unpacked subcharts (`charts/*`)
	subchartsDir := filepath.Join(chartDir, "charts")
	if entries, err := os.ReadDir(subchartsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			subKey := e.Name()
			if alias, ok := depAliases[subKey]; ok && alias != "" {
				subKey = alias
			}
			subPrefix := withPfx([]string{subKey})
			subUsage, sErr := analyzeChartUsageWithPrefix(filepath.Join(subchartsDir, e.Name()), subPrefix)
			if sErr == nil {
				if subUsage.DynamicRoot {
					u.MarkWildcardSegs(subPrefix)
				}
				for k := range subUsage.ExactPaths {
					u.ExactPaths[k] = true
				}
				for k := range subUsage.WildcardPrefixes {
					u.WildcardPrefixes[k] = true
				}
				for k, msg := range subUsage.RequiredPaths {
					u.RequiredPaths[k] = msg
				}
				for k := range subUsage.NonEmptyDefaults {
					u.NonEmptyDefaults[k] = true
				}
			}
		}
	}

	return u, nil
}

func extractChartDependencies(raw []byte, u *ChartUsage, withPfx func([]string) []string) map[string]string {
	aliases := make(map[string]string)
	var meta struct {
		Dependencies []struct {
			Name      string `yaml:"name"`
			Alias     string `yaml:"alias"`
			Condition string `yaml:"condition"`
		} `yaml:"dependencies"`
	}
	if yaml.Unmarshal(raw, &meta) != nil {
		return aliases
	}
	for _, d := range meta.Dependencies {
		key := d.Name
		if d.Alias != "" {
			aliases[d.Name] = d.Alias
			key = d.Alias
		}
		if key != "" {
			u.MarkExactSegs(withPfx([]string{key}))
		}
		for _, cond := range strings.Split(d.Condition, ",") {
			cond = strings.TrimSpace(cond)
			if cond != "" {
				u.MarkExactSegs(withPfx(strings.Split(cond, ".")))
			}
		}
	}
	return aliases
}

// wholeMapFuncs dynamically serialize, clone, merge, or iterate an entire map subtree (excluding only named keys in `omit`).
var wholeMapFuncs = map[string]bool{
	"toYaml": true, "toJson": true, "toToml": true, "tpl": true,
	"merge": true, "mergeOverwrite": true, "mustMerge": true, "mustMergeOverwrite": true,
	"omit": true, "deepCopy": true, "mustDeepCopy": true,
	"keys": true, "values": true,
}

func extractTemplateValues(src string, u *ChartUsage, withPfx func([]string) []string) {
	tr := parse.New("tmpl")
	tr.Mode = parse.SkipFuncCheck
	trees := make(map[string]*parse.Tree)
	if _, err := tr.Parse(src, "{{", "}}", trees); err != nil {
		fallbackLexScan(src, u, withPfx)
		return
	}
	for _, t := range trees {
		if t == nil || t.Root == nil {
			continue
		}
		scopes := []scopeFrame{{isRoot: true, vars: map[string]varBinding{"$": {isRoot: true}}}}
		walkNode(t.Root, &scopes, u, withPfx)
	}
}

type varBinding struct {
	isRoot  bool
	isKnown bool
	segs    []string
}

type scopeFrame struct {
	isRoot  bool
	isKnown bool
	dotSegs []string
	vars    map[string]varBinding
}

func currentDot(scopes *[]scopeFrame) varBinding {
	top := (*scopes)[len(*scopes)-1]
	return varBinding{isRoot: top.isRoot, isKnown: top.isKnown, segs: top.dotSegs}
}

func lookupVar(scopes *[]scopeFrame, name string) varBinding {
	for i := len(*scopes) - 1; i >= 0; i-- {
		if v, ok := (*scopes)[i].vars[name]; ok {
			return v
		}
	}
	return varBinding{}
}

func setVar(scopes *[]scopeFrame, name string, b varBinding) {
	(*scopes)[len(*scopes)-1].vars[name] = b
}

func resolveChain(base varBinding, ident []string) ([]string, bool) {
	if base.isRoot {
		if len(ident) == 0 || ident[0] != "Values" {
			return nil, false
		}
		return append([]string(nil), ident[1:]...), true
	}
	if !base.isKnown {
		return nil, false
	}
	out := make([]string, 0, len(base.segs)+len(ident))
	out = append(out, base.segs...)
	out = append(out, ident...)
	return out, true
}

func exprValuesSegs(n parse.Node, scopes *[]scopeFrame) ([]string, bool) {
	switch x := n.(type) {
	case *parse.FieldNode:
		return resolveChain(currentDot(scopes), x.Ident)
	case *parse.VariableNode:
		if len(x.Ident) == 0 {
			return nil, false
		}
		base := lookupVar(scopes, x.Ident[0])
		return resolveChain(base, x.Ident[1:])
	case *parse.DotNode:
		d := currentDot(scopes)
		if d.isKnown && !d.isRoot {
			return append([]string(nil), d.segs...), true
		}
	case *parse.PipeNode:
		if len(x.Cmds) == 1 && len(x.Cmds[0].Args) == 1 {
			return exprValuesSegs(x.Cmds[0].Args[0], scopes)
		}
	}
	return nil, false
}

func walkNode(n parse.Node, scopes *[]scopeFrame, u *ChartUsage, withPfx func([]string) []string) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, child := range x.Nodes {
			walkNode(child, scopes, u, withPfx)
		}
	case *parse.ActionNode:
		walkPipe(x.Pipe, scopes, u, withPfx)
	case *parse.IfNode:
		walkPipe(x.Pipe, scopes, u, withPfx)
		walkNode(x.List, scopes, u, withPfx)
		walkNode(x.ElseList, scopes, u, withPfx)
	case *parse.WithNode:
		var target varBinding
		if x.Pipe != nil && len(x.Pipe.Cmds) > 0 {
			lastCmd := x.Pipe.Cmds[len(x.Pipe.Cmds)-1]
			if len(lastCmd.Args) == 1 {
				if vp, ok := exprValuesSegs(lastCmd.Args[0], scopes); ok {
					target = varBinding{isKnown: true, segs: vp}
				}
			}
		}
		walkPipe(x.Pipe, scopes, u, withPfx)
		pushScope(scopes, target)
		if x.Pipe != nil {
			for _, decl := range x.Pipe.Decl {
				if len(decl.Ident) > 0 {
					setVar(scopes, decl.Ident[0], target)
				}
			}
		}
		walkNode(x.List, scopes, u, withPfx)
		popScope(scopes)
		walkNode(x.ElseList, scopes, u, withPfx)
	case *parse.RangeNode:
		if x.Pipe != nil && len(x.Pipe.Cmds) > 0 {
			lastCmd := x.Pipe.Cmds[len(x.Pipe.Cmds)-1]
			if len(lastCmd.Args) == 1 {
				if vp, ok := exprValuesSegs(lastCmd.Args[0], scopes); ok {
					u.MarkWildcardSegs(withPfx(vp))
				}
			}
		}
		walkPipe(x.Pipe, scopes, u, withPfx)
		pushScope(scopes, varBinding{})
		walkNode(x.List, scopes, u, withPfx)
		popScope(scopes)
		walkNode(x.ElseList, scopes, u, withPfx)
	case *parse.TemplateNode:
		walkPipe(x.Pipe, scopes, u, withPfx)
	}
}

func pushScope(scopes *[]scopeFrame, dot varBinding) {
	*scopes = append(*scopes, scopeFrame{
		isRoot:  dot.isRoot,
		isKnown: dot.isKnown,
		dotSegs: dot.segs,
		vars:    make(map[string]varBinding),
	})
}

func popScope(scopes *[]scopeFrame) {
	if len(*scopes) > 1 {
		*scopes = (*scopes)[:len(*scopes)-1]
	}
}

func walkPipe(p *parse.PipeNode, scopes *[]scopeFrame, u *ChartUsage, withPfx func([]string) []string) {
	if p == nil {
		return
	}
	var pipeProducesSegs []string
	var pipeHasValues bool

	for idx, cmd := range p.Cmds {
		if len(cmd.Args) == 0 {
			continue
		}
		fnName := ""
		if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok {
			fnName = id.Ident
		}

		// `required "error msg" .Values.foo.bar` (or `.Values.foo.bar | required "msg"`)
		if fnName == "required" {
			msg := "required value must be set"
			if len(cmd.Args) >= 2 {
				if s, ok := cmd.Args[1].(*parse.StringNode); ok {
					msg = s.Text
				}
			}
			var reqSegs []string
			var reqOk bool
			if len(cmd.Args) >= 3 {
				reqSegs, reqOk = exprValuesSegs(cmd.Args[2], scopes)
			} else if idx > 0 && pipeHasValues {
				reqSegs, reqOk = pipeProducesSegs, true
			}
			// Only unconditional top-level `required` (scope depth == 1) guarantees `helm template` fails without `-f`/`--set`;
			// conditional `{{- if .Values.feat.enabled }}{{ required ... }}{{- end }}` is only required when enabled!
			if reqOk && len(reqSegs) > 0 && len(*scopes) == 1 {
				full := withPfx(reqSegs)
				u.MarkExactSegs(full)
				if !u.NonEmptyDefaults[joinSegs(full)] {
					u.RequiredPaths[joinSegs(full)] = msg
				}
			}
		}

		// `index .Values "foo" "bar.baz"`
		if fnName == "index" && len(cmd.Args) >= 2 {
			if vp, ok := exprValuesSegs(cmd.Args[1], scopes); ok {
				sub := append([]string(nil), vp...)
				allStatic := true
				for _, arg := range cmd.Args[2:] {
					if s, ok := arg.(*parse.StringNode); ok {
						sub = append(sub, s.Text)
					} else {
						allStatic = false
						break
					}
				}
				if allStatic {
					u.MarkExactSegs(withPfx(sub))
					pipeProducesSegs = sub
					pipeHasValues = true
				} else {
					u.MarkWildcardSegs(withPfx(sub))
				}
			}
		}

		// `get .Values.foo "bar"` or `hasKey .Values.foo "bar"`
		if (fnName == "hasKey" || fnName == "get") && len(cmd.Args) == 3 {
			if vp, ok := exprValuesSegs(cmd.Args[1], scopes); ok {
				if s, ok := cmd.Args[2].(*parse.StringNode); ok {
					sub := append(append([]string(nil), vp...), s.Text)
					u.MarkExactSegs(withPfx(sub))
				} else {
					u.MarkWildcardSegs(withPfx(vp))
				}
			}
		}

		// `pick .Values.foo "a" "b"` -> marks ONLY `foo.a` and `foo.b` exact (not wildcard!)
		if fnName == "pick" && len(cmd.Args) >= 3 {
			if vp, ok := exprValuesSegs(cmd.Args[1], scopes); ok {
				for _, arg := range cmd.Args[2:] {
					if s, ok := arg.(*parse.StringNode); ok {
						sub := append(append([]string(nil), vp...), s.Text)
						u.MarkExactSegs(withPfx(sub))
					} else {
						u.MarkWildcardSegs(withPfx(vp))
					}
				}
			}
		}

		// `dig "a" "b" default .Values`
		if fnName == "dig" && len(cmd.Args) >= 3 {
			lastArg := cmd.Args[len(cmd.Args)-1]
			if vp, ok := exprValuesSegs(lastArg, scopes); ok {
				sub := append([]string(nil), vp...)
				allStatic := true
				for _, arg := range cmd.Args[1 : len(cmd.Args)-2] {
					if s, ok := arg.(*parse.StringNode); ok {
						sub = append(sub, s.Text)
					} else {
						allStatic = false
						break
					}
				}
				if allStatic {
					u.MarkExactSegs(withPfx(sub))
				} else {
					u.MarkWildcardSegs(withPfx(sub))
				}
			}
		}

		if wholeMapFuncs[fnName] {
			if idx > 0 && pipeHasValues {
				u.MarkWildcardSegs(withPfx(pipeProducesSegs))
			}
			for _, arg := range cmd.Args[1:] {
				if vp, ok := exprValuesSegs(arg, scopes); ok {
					u.MarkWildcardSegs(withPfx(vp))
				}
			}
		}

		for _, arg := range cmd.Args {
			switch a := arg.(type) {
			case *parse.PipeNode:
				walkPipe(a, scopes, u, withPfx)
			default:
				if vp, ok := exprValuesSegs(a, scopes); ok {
					if len(vp) > 0 {
						u.MarkExactSegs(withPfx(vp))
					}
					if len(cmd.Args) == 1 {
						pipeProducesSegs = vp
						pipeHasValues = true
					}
				}
			}
		}
	}

	if len(p.Decl) == 1 && len(p.Cmds) == 1 && len(p.Cmds[0].Args) == 1 {
		if vp, ok := exprValuesSegs(p.Cmds[0].Args[0], scopes); ok {
			setVar(scopes, p.Decl[0].Ident[0], varBinding{isKnown: true, segs: vp})
		}
	}
}

func fallbackLexScan(src string, u *ChartUsage, withPfx func([]string) []string) {
	idx := 0
	for {
		pos := strings.Index(src[idx:], ".Values.")
		if pos == -1 {
			break
		}
		start := idx + pos + len(".Values.")
		end := start
		for end < len(src) {
			ch := src[end]
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' || ch == '.' {
				end++
			} else {
				break
			}
		}
		path := strings.Trim(src[start:end], ".")
		if path != "" {
			u.MarkExactSegs(withPfx(strings.Split(path, ".")))
		}
		idx = end
	}
}

func walkDefaultValuesNode(n *yaml.Node, prefix []string, u *ChartUsage, withPfx func([]string) []string) {
	if n == nil {
		return
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		walkDefaultValuesNode(n.Content[0], prefix, u, withPfx)
		return
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	if len(n.Content) == 0 && len(prefix) > 0 {
		u.MarkWildcardSegs(withPfx(prefix))
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		vNode := n.Content[i+1]
		next := append(append([]string(nil), prefix...), k)
		full := withPfx(next)
		u.MarkExactSegs(full)
		if vNode.Kind == yaml.ScalarNode && vNode.Tag != "!!null" && vNode.Value != "" {
			u.NonEmptyDefaults[joinSegs(full)] = true
		}
		if vNode.Kind == yaml.MappingNode {
			walkDefaultValuesNode(vNode, next, u, withPfx)
		}
	}
}

func walkJSONSchema(schema map[string]any, prefix []string, u *ChartUsage, withPfx func([]string) []string) {
	if props, ok := schema["properties"].(map[string]any); ok {
		for k, v := range props {
			next := append(append([]string(nil), prefix...), k)
			u.MarkExactSegs(withPfx(next))
			if sub, ok := v.(map[string]any); ok {
				walkJSONSchema(sub, next, u, withPfx)
			}
		}
	}
	if addl, ok := schema["additionalProperties"]; ok {
		switch a := addl.(type) {
		case bool:
			if a {
				u.MarkWildcardSegs(withPfx(prefix))
			}
		case map[string]any:
			u.MarkWildcardSegs(withPfx(prefix))
		}
	}
}

// ExtractUserValueLeaves extracts all concrete leaf segment chains set in a user's `values.yaml` override file.
func ExtractUserValueLeaves(valuesFile string) ([][]string, map[string]bool, error) {
	raw, err := os.ReadFile(valuesFile)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("invalid YAML in %s: %w", valuesFile, err)
	}
	var leaves [][]string
	nonEmpty := make(map[string]bool)
	collectLeafSegs(&doc, nil, &leaves, nonEmpty)
	sort.Slice(leaves, func(i, j int) bool {
		return joinSegs(leaves[i]) < joinSegs(leaves[j])
	})
	return leaves, nonEmpty, nil
}

func collectLeafSegs(n *yaml.Node, prefix []string, out *[][]string, nonEmpty map[string]bool) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			collectLeafSegs(c, prefix, out, nonEmpty)
		}
	case yaml.MappingNode:
		if len(n.Content) == 0 {
			if len(prefix) > 0 {
				cp := append([]string(nil), prefix...)
				*out = append(*out, cp)
			}
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			v := n.Content[i+1]
			next := append(append([]string(nil), prefix...), k)
			collectLeafSegs(v, next, out, nonEmpty)
		}
	default:
		if len(prefix) > 0 {
			cp := append([]string(nil), prefix...)
			*out = append(*out, cp)
			if n.Tag != "!!null" && n.Value != "" {
				nonEmpty[joinSegs(cp)] = true
			}
		}
	}
}

// PlanRequiredPlaceholders inspects `required` `.Values.*` calls in base and head charts:
//   - If `head` introduces a NEW `required` path that wasn't required in `base` and isn't supplied by user `-f`/`--set`:
//     returns a SeverityBlocker finding (`helm.values.new_required_unset`) because upgrading will break deploy!
//   - If a `required` path already existed in `base` (or single-chart check without `--base-chart`, e.g. Terraform-injected secret),
//     returns deterministic `--set-string` placeholders so static rendering can still inspect all templates cleanly!
func PlanRequiredPlaceholders(baseChartDir, headChartDir string, valuesFiles []string, explicitSets []string) (placeholders []string, newRequiredBlockers []verdict.Finding) {
	var baseUsage *ChartUsage
	if baseChartDir != "" {
		baseUsage, _ = AnalyzeChartUsage(baseChartDir)
	}
	headUsage, err := AnalyzeChartUsage(headChartDir)
	if err != nil || headUsage == nil {
		return nil, nil
	}

	userSet := make(map[string]bool)
	for _, vf := range valuesFiles {
		_, nonEmpty, lErr := ExtractUserValueLeaves(vf)
		if lErr == nil {
			for k := range nonEmpty {
				userSet[k] = true
			}
		}
	}
	for _, kv := range explicitSets {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			k := strings.TrimSpace(kv[:eq])
			v := kv[eq+1:]
			if v != "" {
				userSet[joinSegs(strings.Split(k, "."))] = true
			}
		}
	}

	var keys []string
	for k := range headUsage.RequiredPaths {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if userSet[k] || headUsage.NonEmptyDefaults[k] {
			continue
		}
		segs := splitSegs(k)
		humanKey := FormatKeyPath(segs)
		dotSetKey := strings.Join(segs, ".")

		if baseUsage != nil {
			_, existedInBase := baseUsage.RequiredPaths[k]
			if !existedInBase {
				// Brand-new required value introduced by upgrade!
				newRequiredBlockers = append(newRequiredBlockers, verdict.Finding{
					RuleID:   "helm.values.new_required_unset",
					Severity: verdict.SeverityBlocker,
					Resource: humanKey,
					Message:  fmt.Sprintf("Upgraded chart introduces a NEW `required` value `%s` that is unset in your values.", humanKey),
					Detail:   fmt.Sprintf("Template requirement: %q", headUsage.RequiredPaths[k]),
				})
				continue
			}
		}
		placeholders = append(placeholders, dotSetKey+"=stageproof-placeholder")
	}
	return placeholders, newRequiredBlockers
}

// DiffValuesConsumption compares user-supplied `values.yaml` paths against baseChartDir and headChartDir.
func DiffValuesConsumption(baseChartDir, headChartDir string, valuesFiles []string) ([]verdict.Finding, *ChartUsage, *ChartUsage, error) {
	var baseUsage *ChartUsage
	if baseChartDir != "" {
		var err error
		baseUsage, err = AnalyzeChartUsage(baseChartDir)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("analyze base chart %s: %w", baseChartDir, err)
		}
	}
	headUsage, err := AnalyzeChartUsage(headChartDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("analyze head chart %s: %w", headChartDir, err)
	}

	markUnvendoredSubcharts(baseChartDir, baseUsage)
	markUnvendoredSubcharts(headChartDir, headUsage)

	var findings []verdict.Finding
	for _, vf := range valuesFiles {
		leaves, _, lErr := ExtractUserValueLeaves(vf)
		if lErr != nil {
			return nil, baseUsage, headUsage, lErr
		}
		for _, segs := range leaves {
			if headUsage.ConsumesSegs(segs) {
				continue
			}
			inBase := baseUsage != nil && baseUsage.ConsumesSegs(segs)
			humanKey := FormatKeyPath(segs)
			suggestion := closestKeySuggestion(segs, headUsage.ExactPaths)
			detail := ""
			if suggestion != "" {
				detail = fmt.Sprintf("Possible renamed target in new chart: `%s`", suggestion)
			}

			if inBase || baseUsage == nil {
				msg := fmt.Sprintf("Values key `%s` was consumed in baseline chart but is silently ignored in upgraded chart.", humanKey)
				if baseUsage == nil {
					msg = fmt.Sprintf("Values key `%s` is not referenced anywhere in chart templates, default values.yaml, or values.schema.json.", humanKey)
				}
				findings = append(findings, verdict.Finding{
					RuleID:   "helm.values.silently_dropped",
					Severity: verdict.SeverityBlocker,
					File:     vf,
					Resource: humanKey,
					Message:  msg,
					Detail:   detail,
				})
			} else {
				findings = append(findings, verdict.Finding{
					RuleID:   "helm.values.preexisting_orphan",
					Severity: verdict.SeverityWarning,
					File:     vf,
					Resource: humanKey,
					Message:  fmt.Sprintf("Values key `%s` was already unused in baseline and remains unused in head.", humanKey),
					Detail:   detail,
				})
			}
		}
	}
	return findings, baseUsage, headUsage, nil
}

func markUnvendoredSubcharts(chartDir string, u *ChartUsage) {
	if chartDir == "" || u == nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
	if err != nil {
		return
	}
	var meta struct {
		Dependencies []struct {
			Name  string `yaml:"name"`
			Alias string `yaml:"alias"`
		} `yaml:"dependencies"`
	}
	if yaml.Unmarshal(raw, &meta) != nil {
		return
	}
	for _, d := range meta.Dependencies {
		subPath := filepath.Join(chartDir, "charts", d.Name)
		if info, sErr := os.Stat(subPath); sErr != nil || !info.IsDir() {
			key := d.Name
			if d.Alias != "" {
				key = d.Alias
			}
			if key != "" {
				u.MarkWildcardSegs([]string{key})
			}
		}
	}
}

func closestKeySuggestion(missingSegs []string, candidates map[string]bool) string {
	if len(missingSegs) == 0 {
		return ""
	}
	leaf := missingSegs[len(missingSegs)-1]
	// Generic boolean/resource leaf names (`enabled`, `create`, `name`, `tag`, `image`, `port`, `replicas`)
	// appear dozens of times across unrelated components. Only suggest a rename when at least one parent segment also matches
	// or the leaf identifier itself is specific!
	genericLeaves := map[string]bool{
		"enabled": true, "create": true, "name": true, "tag": true,
		"repository": true, "port": true, "replicas": true, "type": true,
		"annotations": true, "labels": true, "hosts": true, "path": true,
	}
	var best string
	for cand := range candidates {
		cSegs := splitSegs(cand)
		if len(cSegs) == 0 || cSegs[len(cSegs)-1] != leaf {
			continue
		}
		if genericLeaves[leaf] {
			if len(missingSegs) < 2 || len(cSegs) < 2 {
				continue
			}
			// Require the component/parent segment to share a substring (e.g. `alertmanager` in both paths)
			wantParent := strings.ToLower(missingSegs[0])
			gotParent := strings.ToLower(cSegs[0])
			if wantParent != gotParent && !strings.Contains(gotParent, wantParent) && !strings.Contains(wantParent, gotParent) {
				continue
			}
		}
		formatted := FormatKeyPath(cSegs)
		if best == "" || len(formatted) < len(best) {
			best = formatted
		}
	}
	return best
}
