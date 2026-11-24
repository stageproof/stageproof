package helm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	repairPlaceholder  = "stageproof-placeholder"
	repairMaxAttempts  = 24
	repairMaxFileBytes = 4 << 20
)

// Repair is a render retry. SetStrings and Sets fill values the chart's own
// defaults reject. Remove drops a previous placeholder whose type Helm
// rejected. APIVersions answers a template that checks Capabilities.APIVersions.
// SkipSchema is set when the schema document itself does not validate. A value
// the caller already set is never replaced.
type Repair struct {
	SetStrings  []string
	Sets        []string
	SetJSON     []string
	Remove      []string
	APIVersions []string
	SkipSchema  bool
}

func (r Repair) Empty() bool {
	return len(r.SetStrings) == 0 && len(r.Sets) == 0 && len(r.SetJSON) == 0 && len(r.Remove) == 0 && len(r.APIVersions) == 0 && !r.SkipSchema
}

func (r Repair) changesValues() bool {
	return len(r.SetStrings) > 0 || len(r.Sets) > 0 || len(r.SetJSON) > 0
}

// Keys is the values paths this repair fills, in assignment order.
func (r Repair) Keys() []string {
	var keys []string
	seen := map[string]bool{}
	for _, assignment := range append(append(append([]string{}, r.SetStrings...), r.Sets...), r.SetJSON...) {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
}

// CompleteCharts renders the baseline and the new chart. When the baseline
// does not render because a value is empty, a schema field is null, the schema
// document is invalid, or a template names a missing CRD, the same fix is
// applied to both charts and the render is tried again. A failure that appears
// only on the new chart is left for the caller, except a missing CRD or an
// invalid schema document.
func CompleteCharts(ctx context.Context, bin string, base, head RenderOptions) (string, string, Repair, error, error) {
	var repair Repair
	seen := map[string]bool{}
	var baseYAML, headYAML string
	var baseErr, headErr error
	for attempt := 0; attempt < repairMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return "", "", repair, ctx.Err(), ctx.Err()
		}
		state := repair.state()
		if seen[state] {
			break
		}
		seen[state] = true
		baseYAML, headYAML = "", ""
		baseErr, headErr = nil, nil
		if base.ChartDir != "" {
			baseYAML, baseErr = RenderChartBinary(ctx, bin, withRepair(base, repair))
		}
		if baseErr == nil {
			headYAML, headErr = RenderChartBinary(ctx, bin, withRepair(head, repair))
		}
		if baseErr == nil && headErr == nil {
			return baseYAML, headYAML, repair, nil, nil
		}
		failingDir := base.ChartDir
		failing := baseErr
		orig := base
		headOnly := false
		if baseErr == nil {
			failingDir = head.ChartDir
			failing = headErr
			orig = head
			headOnly = base.ChartDir != ""
		}
		// Propose from the caller's values, not from placeholders this loop
		// already added. A later Helm error can replace a placeholder with the
		// type or the choice the chart actually accepts.
		proposal := ProposeRepair(failingDir, failing, orig)
		if headOnly {
			proposal.SetStrings = nil
			proposal.Sets = nil
			proposal.SetJSON = nil
			proposal.Remove = nil
		}
		if proposal.Empty() || !repair.merge(proposal) {
			break
		}
	}
	return baseYAML, headYAML, repair, baseErr, headErr
}

func withRepair(opts RenderOptions, repair Repair) RenderOptions {
	// A previous placeholder can name a parent map. A child assignment has to
	// replace that placeholder or Helm overwrites the map with a string.
	// Remove also drops a placeholder the caller already put on the command
	// when a later render proved that placeholder has the wrong shape.
	drop := map[string]bool{}
	for _, key := range repair.Remove {
		drop[key] = true
	}
	opts.SetValues = append(dropKeys(withoutParentKeys(opts.SetValues, repair.Keys()), drop), repair.Sets...)
	opts.SetStrings = append(dropKeys(withoutParentKeys(opts.SetStrings, repair.Keys()), drop), repair.SetStrings...)
	opts.SetJSON = append(dropKeys(withoutParentKeys(opts.SetJSON, repair.Keys()), drop), repair.SetJSON...)
	opts.APIVersions = mergeStrings(append([]string{}, opts.APIVersions...), repair.APIVersions)
	if repair.SkipSchema {
		opts.SkipSchemaValidation = true
	}
	return opts
}

func withoutParentKeys(assignments, childKeys []string) []string {
	var out []string
	for _, assignment := range assignments {
		key, _, _ := strings.Cut(assignment, "=")
		drop := false
		for _, child := range childKeys {
			if strings.HasPrefix(child, key+".") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, assignment)
		}
	}
	return out
}

func (r Repair) state() string {
	parts := append([]string{}, r.SetStrings...)
	parts = append(parts, r.Sets...)
	parts = append(parts, r.SetJSON...)
	parts = append(parts, r.APIVersions...)
	if r.SkipSchema {
		parts = append(parts, "skip-schema")
	}
	removed := append([]string{}, r.Remove...)
	sort.Strings(removed)
	parts = append(parts, removed...)
	return strings.Join(parts, "\n")
}

func (r *Repair) merge(add Repair) bool {
	before := r.state()
	if add.SkipSchema {
		r.SkipSchema = true
	}
	readded := map[string]bool{}
	for _, key := range add.Keys() {
		readded[key] = true
	}
	seen := map[string]bool{}
	var removed []string
	for _, key := range append(append([]string{}, r.Remove...), add.Remove...) {
		if key == "" || readded[key] || seen[key] {
			continue
		}
		seen[key] = true
		removed = append(removed, key)
	}
	sort.Strings(removed)
	r.Remove = removed
	drop := map[string]bool{}
	for key := range seen {
		drop[key] = true
	}
	for key := range readded {
		drop[key] = true
	}
	r.SetStrings = dropKeys(r.SetStrings, drop)
	r.Sets = dropKeys(r.Sets, drop)
	r.SetJSON = dropKeys(r.SetJSON, drop)
	r.SetStrings = mergeAssignments(r.SetStrings, add.SetStrings)
	r.Sets = mergeAssignments(r.Sets, add.Sets)
	r.SetJSON = mergeAssignments(r.SetJSON, add.SetJSON)
	r.APIVersions = mergeStrings(r.APIVersions, add.APIVersions)
	r.dropShadowedParents()
	return r.state() != before
}

// dropShadowedParents removes a filled parent when a later assignment fills a
// child. `--set-string driver.config=placeholder` would replace the map the
// child assignment needs.
func (r *Repair) dropShadowedParents() {
	keys := map[string]bool{}
	for _, key := range r.Keys() {
		keys[key] = true
	}
	drop := map[string]bool{}
	for key := range keys {
		for other := range keys {
			if other != key && strings.HasPrefix(other, key+".") {
				drop[key] = true
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	r.SetStrings = dropKeys(r.SetStrings, drop)
	r.Sets = dropKeys(r.Sets, drop)
	r.SetJSON = dropKeys(r.SetJSON, drop)
}

func dropKeys(assignments []string, drop map[string]bool) []string {
	var out []string
	for _, assignment := range assignments {
		key, _, _ := strings.Cut(assignment, "=")
		if drop[key] {
			continue
		}
		out = append(out, assignment)
	}
	return out
}

func mergeAssignments(have, add []string) []string {
	seen := map[string]bool{}
	for _, assignment := range have {
		key, _, _ := strings.Cut(assignment, "=")
		seen[key] = true
	}
	for _, assignment := range add {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		have = append(have, assignment)
	}
	return have
}

func mergeStrings(have, add []string) []string {
	seen := map[string]bool{}
	for _, item := range have {
		seen[item] = true
	}
	for _, item := range add {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		have = append(have, item)
	}
	sort.Strings(have)
	return have
}

var (
	schemaIssuePattern = regexp.MustCompile(`at '([^']*)':\s*(.+)`)
	valuesPathPattern  = regexp.MustCompile(`\$?\.Values((?:\.[A-Za-z0-9_]+)+)`)
	apiVersionPattern  = regexp.MustCompile(`\b([a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)+/v[0-9][A-Za-z0-9]*/[A-Z][A-Za-z0-9]+)\b`)
	requiredVarPattern = regexp.MustCompile(`required\s+"[^"]*"\s+\$([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z0-9_]+)+)`)
	trueAssignment     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_.-]*)=true\b`)
	setBothTrue        = regexp.MustCompile(`([A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+) and ([A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+) to true`)
	setEqualsTrue      = regexp.MustCompile(`(?:set\s+|--set\s+)'?([A-Za-z_][A-Za-z0-9_.-]*)=true'?\b`)
	mustBeFalse        = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.-]*) must be false`)
	eitherSetting      = regexp.MustCompile(`setting "([^"]+)" or "([^"]+)"`)
	unsetKey           = regexp.MustCompile(`unset '([^']+)'`)
	eitherSet          = regexp.MustCompile(`(?i)either set ([A-Za-z_][A-Za-z0-9_.-]*)`)
	errorSubchart      = regexp.MustCompile(`charts/([A-Za-z0-9_.-]+)/`)
	typeMismatch       = regexp.MustCompile(`type mismatch on ([A-Za-z0-9_.-]+)`)
	execLocation       = regexp.MustCompile(`(?:template: |\()([^()\s]+):(\d+):\d+`)
	enableChoice       = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_-]*):\s*\n\s*<`)
	dottedPath         = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)+`)
	backtickPath       = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_-]*(?:\\.[A-Za-z_][A-Za-z0-9_-]*)+)`")
	eqLiteralPattern   = regexp.MustCompile(`eq\s+\$?\.Values((?:\.[A-Za-z0-9_]+)+)\s+"([^"]+)"`)
)

// ProposeRepair reads one Helm failure and returns the next retry. The chart
// directory is the chart Helm just rendered.
func ProposeRepair(chartDir string, renderErr error, opts RenderOptions) Repair {
	if renderErr == nil || chartDir == "" {
		return Repair{}
	}
	text := renderErr.Error()
	protected := protectedKeys(opts)
	var repair Repair
	if strings.Contains(text, "not valid against metaschema") {
		repair.SkipSchema = true
		return repair
	}
	repair.APIVersions = apiVersions(text)
	repair.addSchemaIssues(chartDir, text, protected)
	repair.addHelmType(text, protected)
	repair.addRenderSite(chartDir, text, opts, protected)
	if repair.changesValues() || strings.Contains(text, "type mismatch on ") || (strings.Contains(text, "can't evaluate field") && len(repair.Remove) > 0) {
		repair.sanitize()
		return repair
	}
	paths := valuesPaths(text)
	literals := map[string]string{}
	if site, fileText, ok := templateSite(chartDir, helmMessage(text)); ok {
		found, foundLiterals := pathsInSite(fileText, site)
		for path, value := range foundLiterals {
			literals[prefixPath(valuesPrefix(chartDir, site.file), path)] = value
		}
		if len(found) > 0 {
			prefix := valuesPrefix(chartDir, site.file)
			paths = nil
			for _, path := range found {
				paths = append(paths, prefixPath(prefix, path))
			}
		}
	}
	repair.addValuePaths(chartDir, text, paths, literals, protected)
	repair.addEnabledChoice(chartDir, text, protected)
	repair.sanitize()
	return repair
}

func (r *Repair) addSchemaIssues(chartDir, text string, protected map[string]bool) {
	var assignments []string
	var typed []string
	var jsonOut []string
	root := chartName(chartDir)
	block := root
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if name, ok := schemaBlockName(trim); ok {
			block = name
			continue
		}
		match := schemaIssuePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		path := pointerPath(match[1])
		if path == "" {
			continue
		}
		if block != "" && root != "" && block != root {
			path = chartAlias(chartDir, block) + "." + path
		}
		if protected[path] || alreadySet(path, assignments, typed, jsonOut) {
			continue
		}
		detail := strings.ToLower(match[2])
		if strings.Contains(detail, "validation failed") || strings.Contains(detail, "additional properties") {
			if strings.Contains(detail, "additional properties") {
				r.Remove = append(r.Remove, path+".stageproofPlaceholder")
				if name, value, ok := firstSchemaEnum(chartDir, match[1]); ok {
					choice := path + "." + name
					if !protected[choice] {
						assignments = append(assignments, choice+"="+value)
					}
				}
			}
			continue
		}
		kind, value := schemaValue(match[2])
		switch kind {
		case "array":
			jsonOut = append(jsonOut, path+`=["`+value+`"]`)
			r.Remove = append(r.Remove, path)
		case "object":
			if name, choice, ok := firstSchemaEnum(chartDir, match[1]); ok {
				assignments = append(assignments, path+"."+name+"="+choice)
			} else if !schemaForbidsExtra(chartDir, path) {
				typed = append(typed, path+".stageproofPlaceholder="+value)
			}
			r.Remove = append(r.Remove, path)
		case "number", "bool":
			typed = append(typed, path+"="+value)
			r.Remove = append(r.Remove, path)
		default:
			assignments = append(assignments, path+"="+value)
		}
	}
	r.SetStrings = mergeAssignments(r.SetStrings, assignments)
	r.Sets = mergeAssignments(r.Sets, typed)
	r.SetJSON = mergeAssignments(r.SetJSON, jsonOut)
}

func schemaBlockName(line string) (string, bool) {
	if strings.HasPrefix(line, "-") || strings.Contains(line, "at '") || !strings.HasSuffix(line, ":") {
		return "", false
	}
	name := strings.TrimSuffix(line, ":")
	if !safeRepairSegment(name) {
		return "", false
	}
	return name, true
}

func chartName(dir string) string {
	raw, ok := readChartFile(dir, filepath.Join(dir, "Chart.yaml"))
	if !ok {
		return ""
	}
	var meta struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal(raw, &meta) != nil {
		return ""
	}
	return meta.Name
}

func (r *Repair) addHelmType(text string, protected map[string]bool) {
	paths := valuesPaths(text)
	low := strings.ToLower(text)
	switch {
	case strings.Contains(text, "expected bool"):
		for _, path := range paths {
			if protected[path] {
				continue
			}
			r.Sets = append(r.Sets, path+"=true")
			r.Remove = append(r.Remove, path)
		}
	case strings.Contains(text, "range can't iterate"):
		for _, path := range paths {
			if protected[path] {
				continue
			}
			r.Remove = append(r.Remove, path)
		}
	case strings.Contains(low, "only one of") || strings.Contains(text, " xor ") || strings.Contains(low, "mutually exclusive"):
		chosen := paths
		if len(chosen) < 2 {
			for _, loc := range dottedPath.FindAllStringIndex(text, -1) {
				path := text[loc[0]:loc[1]]
				if strings.Count(path, ".") < 2 {
					continue
				}
				if loc[0] > 0 && (text[loc[0]-1] == '/' || text[loc[0]-1] == '@') {
					continue
				}
				chosen = append(chosen, path)
			}
		}
		if len(chosen) >= 2 && !protected[chosen[0]] {
			r.SetStrings = append(r.SetStrings, chosen[0]+"="+repairPlaceholder)
			for _, path := range chosen[1:] {
				if !protected[path] {
					r.Remove = append(r.Remove, path)
				}
			}
		}
	}
	for _, match := range setBothTrue.FindAllStringSubmatch(text, -1) {
		for _, path := range match[1:] {
			r.addBool(path, "true", protected)
		}
	}
	for _, match := range setEqualsTrue.FindAllStringSubmatch(text, -1) {
		r.addBool(scopePath(text, match[1]), "true", protected)
	}
	for _, match := range mustBeFalse.FindAllStringSubmatch(text, -1) {
		r.addBool(scopePath(text, match[1]), "false", protected)
	}
	for _, match := range eitherSet.FindAllStringSubmatch(text, -1) {
		if match[1] == "" || protected[match[1]] || strings.Count(match[1], ".") < 2 {
			continue
		}
		r.SetStrings = append(r.SetStrings, match[1]+"="+repairPlaceholder)
		r.Remove = append(r.Remove, match[1])
	}
	for _, match := range unsetKey.FindAllStringSubmatch(text, -1) {
		path := scopePath(text, match[1])
		if path != "" && !protected[path] {
			r.Remove = append(r.Remove, path)
		}
	}
	for _, match := range eitherSetting.FindAllStringSubmatch(text, -1) {
		path := match[1]
		if path == "" || protected[path] {
			continue
		}
		r.SetStrings = append(r.SetStrings, path+"="+repairPlaceholder)
		if match[2] != "" && !protected[match[2]] {
			r.Remove = append(r.Remove, match[2])
		}
	}
	if i := strings.Index(strings.ToLower(text), "remove "); i >= 0 {
		for _, path := range dottedPath.FindAllString(text[i:], -1) {
			if protected[path] {
				continue
			}
			r.Remove = append(r.Remove, path)
		}
	}
	for _, match := range backtickPath.FindAllStringSubmatch(text, -1) {
		path := match[1]
		if path == "" || protected[path] {
			continue
		}
		r.SetStrings = append(r.SetStrings, path+"="+repairPlaceholder)
	}
}

func (r *Repair) addValuePaths(chartDir, text string, paths []string, literals map[string]string, protected map[string]bool) {
	removed := map[string]bool{}
	for _, path := range r.Remove {
		removed[path] = true
	}
	leaves := leafPaths(paths)
	var stringsOut []string
	var typed []string
	for _, path := range leaves {
		if path == "" || protected[path] || removed[path] {
			continue
		}
		if wantsTrue(text, path) {
			typed = append(typed, path+"=true")
			r.Remove = append(r.Remove, path)
			continue
		}
		if value := literals[path]; value != "" {
			stringsOut = append(stringsOut, path+"="+value)
			r.Remove = append(r.Remove, path)
			continue
		}
		if strings.HasSuffix(path, ".enabled") && (strings.Contains(strings.ToLower(text), "no longer") || strings.Contains(strings.ToLower(text), "has been removed")) {
			typed = append(typed, path+"=false")
			r.Remove = append(r.Remove, path)
			continue
		}
		if mappingAt(chartDir, path) {
			if name, choice, ok := firstSchemaEnum(chartDir, "/"+strings.ReplaceAll(path, ".", "/")); ok {
				stringsOut = append(stringsOut, path+"."+name+"="+choice)
			} else if !schemaForbidsExtra(chartDir, path) {
				typed = append(typed, path+".stageproofPlaceholder="+repairPlaceholder)
			}
			continue
		}
		stringsOut = append(stringsOut, path+"="+repairPlaceholder)
	}
	r.SetStrings = mergeAssignments(r.SetStrings, stringsOut)
	r.Sets = mergeAssignments(r.Sets, typed)
}

func pointerPath(pointer string) string {
	pointer = strings.TrimSpace(pointer)
	if pointer == "" || pointer == "/" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var segs []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		segs = append(segs, part)
	}
	return strings.Join(segs, ".")
}

func schemaValue(detail string) (kind, value string) {
	low := strings.ToLower(detail)
	switch {
	case strings.Contains(low, "want integer"), strings.Contains(low, "want number"):
		return "number", "1"
	case strings.Contains(low, "want boolean"):
		return "bool", "true"
	case strings.Contains(low, "want array"):
		return "array", repairPlaceholder
	case strings.Contains(low, "want object"):
		return "object", repairPlaceholder
	}
	if quoted := firstQuoted(detail); quoted != "" && (strings.Contains(low, "one of") || strings.Contains(low, "enum")) {
		return "string", quoted
	}
	return "string", repairPlaceholder
}

func firstQuoted(text string) string {
	for _, quote := range []string{`"`, `'`} {
		start := strings.Index(text, quote)
		if start < 0 {
			continue
		}
		rest := text[start+len(quote):]
		end := strings.Index(rest, quote)
		if end > 0 {
			return rest[:end]
		}
	}
	return ""
}

func apiVersions(text string) []string {
	found := apiVersionPattern.FindAllString(text, -1)
	sort.Strings(found)
	return mergeStrings(nil, found)
}

func valuesPaths(text string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, match := range valuesPathPattern.FindAllStringSubmatch(text, -1) {
		path := strings.TrimPrefix(match[1], ".")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func leafPaths(paths []string) []string {
	var leaves []string
	for _, path := range paths {
		shadowed := false
		for _, other := range paths {
			if other != path && strings.HasPrefix(other, path+".") {
				shadowed = true
				break
			}
		}
		if !shadowed {
			leaves = append(leaves, path)
		}
	}
	sort.Strings(leaves)
	return leaves
}

func wantsTrue(text, path string) bool {
	leaf := path
	if dot := strings.LastIndex(path, "."); dot >= 0 {
		leaf = path[dot+1:]
	}
	for _, match := range trueAssignment.FindAllStringSubmatch(text, -1) {
		name := match[1]
		if name == path || name == leaf || strings.HasSuffix(path, "."+name) {
			return true
		}
	}
	return false
}

type templateHit struct {
	file string
	line int
}

func helmMessage(text string) string {
	const marker = "): "
	if i := strings.Index(text, marker); i >= 0 {
		text = text[i+len(marker):]
	}
	if i := strings.Index(text, "\nUse --debug"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

func messageNeedles(message string) []string {
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 16 {
			continue
		}
		if len(line) > 80 {
			line = line[:80]
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	sort.Slice(lines, func(i, j int) bool { return len(lines[i]) > len(lines[j]) })
	if len(lines) > 4 {
		lines = lines[:4]
	}
	return lines
}

func templateSite(chartDir, message string) (templateHit, string, bool) {
	needles := messageNeedles(message)
	for _, needle := range needles {
		hits := findNeedles(chartDir, needle)
		if len(hits) == 0 {
			continue
		}
		// Several checks can share the longest sentence. Prefer the line that
		// also contains the more specific sentences from this error.
		best := hits[0]
		bestExtra := -1
		for _, hit := range hits {
			extra := 0
			for _, other := range needles {
				if other != needle && strings.Contains(hit.line, other) {
					extra++
				}
			}
			if extra > bestExtra {
				bestExtra = extra
				best = hit
			}
		}
		return best.hit, best.text, true
	}
	return templateHit{}, "", false
}

type needleHit struct {
	hit  templateHit
	text string
	line string
}

func findNeedles(chartDir, needle string) []needleHit {
	var hits []needleHit
	_ = filepath.WalkDir(chartDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" && ext != ".tpl" && ext != ".txt" {
			return nil
		}
		buf, ok := readChartFile(chartDir, path)
		if !ok {
			return nil
		}
		body := string(buf)
		if !strings.Contains(body, needle) {
			return nil
		}
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(line, needle) {
				hits = append(hits, needleHit{
					hit:  templateHit{file: path, line: i + 1},
					text: body,
					line: line,
				})
			}
		}
		return nil
	})
	return hits
}

func (r *Repair) addRenderSite(chartDir, text string, opts RenderOptions, protected map[string]bool) {
	for _, match := range typeMismatch.FindAllStringSubmatch(text, -1) {
		path := match[1]
		if path != "" && !protected[path] {
			r.Remove = append(r.Remove, path)
		}
	}
	file, line, ok := executedTemplate(chartDir, text)
	if !ok {
		return
	}
	body, ok := readChartFile(chartDir, file)
	if !ok {
		return
	}
	lines := strings.Split(string(body), "\n")
	if line < 1 || line > len(lines) {
		return
	}
	site := lines[line-1]
	if strings.Contains(text, "expected string") {
		for _, path := range valuesPaths(site) {
			full := prefixPath(valuesPrefix(chartDir, file), path)
			if full == "" || protected[full] {
				continue
			}
			r.SetStrings = append(r.SetStrings, full+"="+repairPlaceholder)
			r.Remove = append(r.Remove, full)
		}
	}
	if !strings.Contains(text, "can't evaluate field") {
		return
	}
	var ranged string
	for i := line - 1; i >= 1 && i >= line-30; i-- {
		if !strings.Contains(lines[i-1], "range") {
			continue
		}
		found := valuesPaths(lines[i-1])
		if len(found) > 0 {
			ranged = found[len(found)-1]
			break
		}
	}
	if ranged == "" {
		return
	}
	full := prefixPath(valuesPrefix(chartDir, file), ranged)
	for _, assignment := range append(append([]string{}, opts.SetStrings...), opts.SetValues...) {
		key, _, cut := strings.Cut(assignment, "=")
		if !cut || protected[key] || !strings.HasPrefix(key, full+".") {
			continue
		}
		rest := strings.TrimPrefix(key, full+".")
		if rest == "" || strings.Contains(rest, ".") {
			continue
		}
		r.Remove = append(r.Remove, key)
	}
}

func (r *Repair) addEnabledChoice(chartDir, text string, protected map[string]bool) {
	if !strings.Contains(strings.ToLower(text), "at least one") || !strings.Contains(text, "enabled") {
		return
	}
	match := enableChoice.FindStringSubmatch(text)
	if match == nil {
		return
	}
	node := yamlAt(chartDir, filepath.Join(chartDir, "values.yaml"), []string{match[1]})
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	var children []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i+1].Kind == yaml.MappingNode {
			children = append(children, node.Content[i].Value)
		}
	}
	sort.Strings(children)
	if len(children) == 0 {
		return
	}
	r.addBool(match[1]+"."+children[0]+".enabled", "true", protected)
}

func executedTemplate(chartDir, text string) (string, int, bool) {
	matches := execLocation.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", 0, false
	}
	match := matches[len(matches)-1]
	line, err := strconv.Atoi(match[2])
	if err != nil {
		return "", 0, false
	}
	quoted := match[1]
	if file, ok := safeChartPath(chartDir, filepath.Join(chartDir, quoted)); ok {
		return file, line, true
	}
	if i := strings.Index(quoted, "/"); i >= 0 {
		if file, ok := safeChartPath(chartDir, filepath.Join(chartDir, quoted[i+1:])); ok {
			return file, line, true
		}
	}
	return "", 0, false
}

func safeChartPath(chartDir, candidate string) (string, bool) {
	if chartDir == "" || candidate == "" {
		return "", false
	}
	root, err := filepath.EvalSymlinks(chartDir)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return "", false
	}
	return resolved, true
}

func readChartFile(chartDir, candidate string) ([]byte, bool) {
	resolved, ok := safeChartPath(chartDir, candidate)
	if !ok {
		return nil, false
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, repairMaxFileBytes+1))
	if err != nil || len(buf) > repairMaxFileBytes {
		return nil, false
	}
	return buf, true
}

func schemaAt(chartDir, pointer string) map[string]any {
	raw, ok := readChartFile(chartDir, filepath.Join(chartDir, "values.schema.json"))
	if !ok {
		return nil
	}
	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	node, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	for _, part := range strings.Split(strings.Trim(pointer, "/"), "/") {
		if part == "" {
			continue
		}
		props, _ := node["properties"].(map[string]any)
		child, found := props[part].(map[string]any)
		if !found {
			return nil
		}
		node = child
	}
	return node
}

func schemaForbidsExtra(chartDir, path string) bool {
	node := schemaAt(chartDir, "/"+strings.ReplaceAll(path, ".", "/"))
	if node == nil {
		return false
	}
	extra, ok := node["additionalProperties"].(bool)
	return ok && !extra
}

func firstSchemaEnum(chartDir, pointer string) (string, string, bool) {
	node := schemaAt(chartDir, pointer)
	if node == nil {
		return "", "", false
	}
	props, _ := node["properties"].(map[string]any)
	var names []string
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec, _ := props[name].(map[string]any)
		enum, _ := spec["enum"].([]any)
		for _, item := range enum {
			value, isString := item.(string)
			if isString && value != "" {
				return name, value, true
			}
		}
	}
	return "", "", false
}

func pathsInSite(fileText string, hit templateHit) ([]string, map[string]string) {
	lines := strings.Split(fileText, "\n")
	if hit.line < 1 || hit.line > len(lines) {
		return nil, nil
	}
	start := hit.line - 6
	if start < 1 {
		start = 1
	}
	window := strings.Join(lines[start-1:hit.line], "\n")
	line := lines[hit.line-1]
	paths := valuesPaths(line)
	paths = append(paths, digPaths(line)...)
	paths = append(paths, variablePaths(fileText, line)...)
	paths = append(paths, indexPaths(line)...)
	literals := eqLiterals(window)
	if len(paths) == 0 {
		paths = append(paths, valuesPaths(window)...)
		paths = append(paths, digPaths(window)...)
		paths = append(paths, variablePaths(fileText, window)...)
		paths = append(paths, indexPaths(window)...)
	}
	for path := range literals {
		paths = append(paths, path)
	}
	return leafPaths(paths), literals
}

func eqLiterals(text string) map[string]string {
	out := map[string]string{}
	for _, match := range eqLiteralPattern.FindAllStringSubmatch(text, -1) {
		path := strings.TrimPrefix(match[1], ".")
		if path == "" {
			continue
		}
		if _, ok := out[path]; !ok {
			out[path] = match[2]
		}
	}
	return out
}

func digPaths(text string) []string {
	var paths []string
	rest := text
	for {
		i := strings.Index(rest, "dig")
		if i < 0 {
			return paths
		}
		chunk := rest[i:]
		rest = rest[i+3:]
		if i > 0 {
			prev := text[len(text)-len(chunk)-1]
			if prev == '_' || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') {
				continue
			}
		}
		quoted, valuesPath, ok := scanDig(chunk)
		if !ok || valuesPath == "" {
			continue
		}
		base := strings.TrimPrefix(strings.TrimPrefix(valuesPath, "$"), ".")
		base = strings.TrimPrefix(base, "Values")
		base = strings.TrimPrefix(base, ".")
		segs := append([]string{}, splitDot(base)...)
		for _, part := range quoted {
			if part == "" {
				break
			}
			segs = append(segs, part)
		}
		if len(segs) > 0 {
			paths = append(paths, strings.Join(segs, "."))
		}
	}
}

func scanDig(chunk string) (quoted []string, valuesPath string, ok bool) {
	i := strings.Index(chunk, `"`)
	if i < 0 {
		return nil, "", false
	}
	for i >= 0 && i < len(chunk) {
		if chunk[i] != '"' {
			break
		}
		end := strings.Index(chunk[i+1:], `"`)
		if end < 0 {
			return nil, "", false
		}
		part := chunk[i+1 : i+1+end]
		next := i + 1 + end + 1
		rest := strings.TrimLeft(chunk[next:], " \t\n")
		if strings.HasPrefix(rest, ".Values") || strings.HasPrefix(rest, "$.Values") {
			// The quoted string immediately before the values map is dig's
			// default, not a path segment.
			field := rest
			if sp := strings.IndexAny(field, " \t\n)|}"); sp >= 0 {
				field = field[:sp]
			}
			return quoted, field, true
		}
		quoted = append(quoted, part)
		i = strings.Index(chunk[next:], `"`)
		if i >= 0 {
			i += next
		}
	}
	return nil, "", false
}

func variablePaths(fileText, window string) []string {
	var paths []string
	for _, match := range requiredVarPattern.FindAllStringSubmatch(window, -1) {
		name, field := match[1], strings.TrimPrefix(match[2], ".")
		lineRe := regexp.MustCompile(`(?m)^.*\$` + name + `\s*:=.*$`)
		line := lineRe.FindString(fileText)
		found := valuesPathPattern.FindAllStringSubmatch(line, -1)
		if len(found) == 0 {
			continue
		}
		base := strings.TrimPrefix(found[len(found)-1][1], ".")
		if base == "" {
			paths = append(paths, field)
		} else {
			paths = append(paths, base+"."+field)
		}
		// A deep copy of the whole values map keeps a root key under that name.
		if strings.Contains(line, "DeepCopy") || strings.Contains(line, "deepCopy") {
			paths = append(paths, field)
		}
	}
	return paths
}

func indexPaths(text string) []string {
	var paths []string
	rest := text
	for {
		i := strings.Index(rest, "index")
		if i < 0 {
			return paths
		}
		chunk := strings.TrimLeft(rest[i+len("index"):], " \t")
		rest = rest[i+len("index"):]
		if !strings.HasPrefix(chunk, ".Values") && !strings.HasPrefix(chunk, "$.Values") {
			continue
		}
		var segs []string
		pos := 0
		if strings.HasPrefix(chunk, "$.Values") {
			pos = len("$.Values")
		} else {
			pos = len(".Values")
		}
		for {
			chunk = strings.TrimLeft(chunk[pos:], " \t")
			if !strings.HasPrefix(chunk, `"`) {
				break
			}
			end := strings.Index(chunk[1:], `"`)
			if end < 0 {
				break
			}
			segs = append(segs, chunk[1:1+end])
			pos = 1 + end + 1
			if pos >= len(chunk) {
				break
			}
			chunk = chunk[pos:]
			pos = 0
		}
		if len(segs) > 0 {
			paths = append(paths, strings.Join(segs, "."))
		}
	}
}

func valuesPrefix(chartDir, file string) string {
	rel, err := filepath.Rel(chartDir, file)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	var segs []string
	parent := chartDir
	for i := 0; i < len(parts); i++ {
		if parts[i] != "charts" || i+1 >= len(parts) {
			if parts[i] == "templates" || parts[i] == "crds" {
				break
			}
			continue
		}
		dirName := parts[i+1]
		segs = append(segs, chartAlias(parent, dirName))
		parent = filepath.Join(parent, "charts", dirName)
		i++
	}
	return strings.Join(segs, ".")
}

func chartAlias(parent, dirName string) string {
	raw, ok := readChartFile(parent, filepath.Join(parent, "Chart.yaml"))
	if !ok {
		return dirName
	}
	var meta struct {
		Dependencies []struct {
			Name  string `yaml:"name"`
			Alias string `yaml:"alias"`
		} `yaml:"dependencies"`
	}
	if yaml.Unmarshal(raw, &meta) != nil {
		return dirName
	}
	for _, dep := range meta.Dependencies {
		if dep.Name == dirName && dep.Alias != "" {
			return dep.Alias
		}
	}
	return dirName
}

func prefixPath(prefix, path string) string {
	// Helm copies the parent global map into every subchart under the same key.
	if prefix == "" || path == "global" || strings.HasPrefix(path, "global.") {
		if path == "" {
			return prefix
		}
		return path
	}
	if path == "" {
		return prefix
	}
	return prefix + "." + path
}

func scopePath(text, path string) string {
	if path == "" || path == "global" || strings.HasPrefix(path, "global.") {
		return path
	}
	match := errorSubchart.FindStringSubmatch(text)
	if match == nil || strings.HasPrefix(path, match[1]+".") {
		return path
	}
	return match[1] + "." + path
}

func (r *Repair) addBool(path, value string, protected map[string]bool) {
	if path == "" || protected[path] {
		return
	}
	r.Sets = append(r.Sets, path+"="+value)
	r.Remove = append(r.Remove, path)
}

func protectedKeys(opts RenderOptions) map[string]bool {
	out := map[string]bool{}
	for _, file := range opts.ValuesFiles {
		_, nonEmpty, err := ExtractUserValueLeaves(file)
		if err != nil {
			continue
		}
		for key := range nonEmpty {
			out[strings.Join(splitSegs(key), ".")] = true
		}
	}
	for _, assignment := range append(append([]string{}, opts.SetValues...), opts.SetStrings...) {
		key, value, ok := strings.Cut(assignment, "=")
		// A placeholder this program invented is not a user value. A later
		// Helm error has to be able to replace it.
		if ok && strings.TrimSpace(value) != "" && value != repairPlaceholder {
			out[key] = true
		}
	}
	return out
}

func alreadySet(path string, groups ...[]string) bool {
	for _, group := range groups {
		for _, assignment := range group {
			key, _, _ := strings.Cut(assignment, "=")
			if key == path {
				return true
			}
		}
	}
	return false
}

func mappingAt(chartDir, path string) bool {
	if chartDir == "" || !safeRepairKey(path) {
		return false
	}
	segs := splitDot(path)
	file := filepath.Join(chartDir, "values.yaml")
	// A subchart value lives in the subchart file under the remaining path.
	if len(segs) == 1 && safeRepairSegment(segs[0]) {
		subDir := filepath.Join(chartDir, "charts", segs[0])
		if root, err := filepath.EvalSymlinks(chartDir); err == nil {
			if resolved, err := filepath.EvalSymlinks(subDir); err == nil {
				if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
					if info, err := os.Stat(resolved); err == nil && info.IsDir() {
						return true
					}
				}
			}
		}
	}
	if len(segs) > 1 && safeRepairSegment(segs[0]) {
		sub := filepath.Join(chartDir, "charts", segs[0], "values.yaml")
		if node := yamlAt(chartDir, sub, segs[1:]); node != nil && node.Kind == yaml.MappingNode {
			return true
		}
	}
	node := yamlAt(chartDir, file, segs)
	return node != nil && node.Kind == yaml.MappingNode
}

func yamlAt(chartDir, path string, segs []string) *yaml.Node {
	raw, ok := readChartFile(chartDir, path)
	if !ok {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var node *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		node = doc.Content[0]
	} else {
		node = &doc
	}
	for _, seg := range segs {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == seg {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return node
}

func splitDot(path string) []string {
	if path == "" {
		return nil
	}
	return strings.Split(path, ".")
}

// sanitize drops any key or value that could split Helm --set* arguments on
// commas, inject extra assignments, or traverse outside the chart tree.
func (r *Repair) sanitize() {
	r.SetStrings = filterSafeAssignments(r.SetStrings, false)
	r.Sets = filterSafeAssignments(r.Sets, false)
	r.SetJSON = filterSafeAssignments(r.SetJSON, true)
	var cleanRemove []string
	for _, key := range r.Remove {
		if safeRepairKey(key) {
			cleanRemove = append(cleanRemove, key)
		}
	}
	r.Remove = cleanRemove
}

func filterSafeAssignments(assignments []string, isJSON bool) []string {
	var out []string
	for _, item := range assignments {
		key, val, ok := strings.Cut(item, "=")
		if !ok || !safeRepairKey(key) {
			continue
		}
		if isJSON {
			if !safeRepairJSON(val) {
				continue
			}
		} else if !safeRepairValue(val) {
			continue
		}
		out = append(out, key+"="+val)
	}
	return out
}

func safeRepairKey(path string) bool {
	if path == "" || len(path) > 256 {
		return false
	}
	for _, seg := range strings.Split(path, ".") {
		if !safeRepairSegment(seg) {
			return false
		}
	}
	return true
}

func safeRepairSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	for _, r := range seg {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func safeRepairValue(val string) bool {
	if val == "" || len(val) > 256 {
		return false
	}
	if strings.ContainsAny(val, ",=\n\r\x00\"'\\{}[]") {
		return false
	}
	return true
}

func safeRepairJSON(val string) bool {
	if !strings.HasPrefix(val, `["`) || !strings.HasSuffix(val, `"]`) {
		return false
	}
	inner := val[2 : len(val)-2]
	return safeRepairValue(inner)
}
