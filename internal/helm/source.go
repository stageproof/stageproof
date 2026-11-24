package helm

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxTarEntries = 2048

var (
	includeCallPattern = regexp.MustCompile(`(?:\binclude|\btemplate)\s+["` + "`" + `]([^"` + "`" + `\s]+)["` + "`" + `]`)
	defineHeadPattern  = regexp.MustCompile(`^\s*define\s+["` + "`" + `]([^"` + "`" + `\s]+)["` + "`" + `]\s*$`)
	blockOpenPattern   = regexp.MustCompile(`^\s*(?:if|range|with|block|define)\b`)
	blockEndPattern    = regexp.MustCompile(`^\s*end\b`)
	negatedPrefixRe    = regexp.MustCompile(`\bnot\s*\(?\s*$`)
)

// TemplateGuardsRemovedAPI reports whether the template file that emitted a
// rendered object tests .Capabilities.APIVersions for apiVersion (or
// apiVersion/kind). helm template includes legacy API versions in its built-in
// capability list and cannot remove them, so a removed-API finding from such a
// guarded template is not proven.
func TemplateGuardsRemovedAPI(chartDir, source, apiVersion, kind string) bool {
	if chartDir == "" || source == "" || apiVersion == "" {
		return false
	}
	texts := templateAndHelpers(chartDir, source)
	if len(texts) == 0 {
		return false
	}
	targets := []string{apiVersion}
	if kind != "" {
		targets = append(targets, apiVersion+"/"+kind)
	}
	for _, body := range texts {
		for _, target := range targets {
			if hasCapabilityCheck(body, target) {
				return true
			}
		}
	}
	return false
}

func hasCapabilityCheck(body, target string) bool {
	q := regexp.QuoteMeta(target)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\$?\.Capabilities\.APIVersions\.Has\s+["` + "`" + `]` + q + `["` + "`" + `]`),
		regexp.MustCompile(`\bhas\s+["` + "`" + `]` + q + `["` + "`" + `]\s+[^}\n]*\.Capabilities\.APIVersions\b`),
	}
	for _, re := range patterns {
		for _, loc := range re.FindAllStringIndex(body, -1) {
			prefix := body[:loc[0]]
			if i := strings.LastIndex(prefix, "{{"); i >= 0 {
				prefix = prefix[i+2:]
			}
			prefix = strings.TrimLeft(prefix, "- ")
			if negatedPrefixRe.MatchString(prefix) {
				continue
			}
			return true
		}
	}
	return false
}

// templateAndHelpers reads the Helm # Source template file and any helper
// definitions it invokes from templates/*.tpl in the same chart (up to depth 4).
func templateAndHelpers(chartDir, source string) []string {
	body, helpers, ok := readTemplateAndHelperFiles(chartDir, source)
	if !ok {
		return nil
	}
	out := []string{body}
	if len(helpers) == 0 {
		return out
	}
	defs := map[string]string{}
	for _, h := range helpers {
		for name, def := range parseDefines(h) {
			if _, exists := defs[name]; !exists {
				defs[name] = def
			}
		}
	}
	if len(defs) == 0 {
		return out
	}
	seen := map[string]bool{}
	frontier := calledHelpers(body)
	for depth := 0; depth < 4 && len(frontier) > 0; depth++ {
		var next []string
		for _, name := range frontier {
			if seen[name] {
				continue
			}
			seen[name] = true
			def, found := defs[name]
			if !found {
				continue
			}
			out = append(out, def)
			next = append(next, calledHelpers(def)...)
		}
		frontier = next
	}
	return out
}

func calledHelpers(body string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range includeCallPattern.FindAllStringSubmatch(body, -1) {
		if m[1] != "" && !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// parseDefines extracts top-level {{ define "name" }}...{{ end }} blocks while
// tracking nested if/range/with/block/end depth.
func parseDefines(text string) map[string]string {
	out := map[string]string{}
	pos := 0
	var activeName string
	activeStart := -1
	depth := 0
	for pos < len(text) {
		open := strings.Index(text[pos:], "{{")
		if open < 0 {
			break
		}
		open += pos
		closeIdx := strings.Index(text[open+2:], "}}")
		if closeIdx < 0 {
			break
		}
		closeEnd := open + 2 + closeIdx + 2
		inner := strings.TrimSpace(text[open+2 : open+2+closeIdx])
		inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(inner, "-"), "-"))
		pos = closeEnd
		if strings.HasPrefix(inner, "/*") {
			continue
		}
		if activeName == "" {
			if m := defineHeadPattern.FindStringSubmatch(inner); m != nil {
				activeName = m[1]
				activeStart = closeEnd
				depth = 1
			}
			continue
		}
		switch {
		case blockOpenPattern.MatchString(inner):
			depth++
		case blockEndPattern.MatchString(inner):
			depth--
			if depth == 0 {
				out[activeName] = text[activeStart:open]
				activeName = ""
				activeStart = -1
			}
		}
	}
	return out
}

// parseSourcePath strips Helm's leading <root-chart-name>/ segment and rejects
// any unsafe path segment.
func parseSourcePath(source string) ([]string, bool) {
	source = strings.TrimSpace(source)
	if source == "" || strings.Contains(source, `\`) {
		return nil, false
	}
	parts := strings.Split(source, "/")
	if len(parts) < 2 {
		return nil, false
	}
	rel := parts[1:]
	for _, seg := range rel {
		if seg == "" || seg == "." || seg == ".." {
			return nil, false
		}
	}
	return rel, true
}

func readTemplateAndHelperFiles(chartDir, source string) (string, []string, bool) {
	rel, ok := parseSourcePath(source)
	if !ok {
		return "", nil, false
	}
	candidate := filepath.Join(append([]string{chartDir}, rel...)...)
	if raw, found := readChartFile(chartDir, candidate); found {
		helpers := readHelperFilesFromDir(chartDir, helperTemplatesDir(chartDir, rel))
		return string(raw), helpers, true
	}
	// Packaged subchart fallback: charts/<subchart>*.tgz.
	if len(rel) >= 3 && rel[0] == "charts" {
		return readTemplateFromSubchartArchive(chartDir, rel[1], strings.Join(rel[2:], "/"))
	}
	return "", nil, false
}

func helperTemplatesDir(chartDir string, rel []string) string {
	for i := len(rel) - 1; i >= 0; i-- {
		if rel[i] == "templates" {
			return filepath.Join(append([]string{chartDir}, rel[:i+1]...)...)
		}
	}
	return ""
}

func readHelperFilesFromDir(chartDir, tmplDir string) []string {
	if tmplDir == "" {
		return nil
	}
	var out []string
	entries, err := os.ReadDir(tmplDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".tpl") {
			continue
		}
		if raw, ok := readChartFile(chartDir, filepath.Join(tmplDir, entry.Name())); ok {
			out = append(out, string(raw))
		}
	}
	return out
}

func readTemplateFromSubchartArchive(chartDir, subName, innerRel string) (string, []string, bool) {
	chartsDir := filepath.Join(chartDir, "charts")
	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return "", nil, false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tgz") {
			continue
		}
		if entry.Name() != subName+".tgz" && !strings.HasPrefix(entry.Name(), subName+"-") {
			continue
		}
		archivePath, ok := safeChartPath(chartDir, filepath.Join(chartsDir, entry.Name()))
		if !ok {
			continue
		}
		body, helpers, found := scanSubchartTar(archivePath, innerRel)
		if found {
			return body, helpers, true
		}
	}
	return "", nil, false
}

func scanSubchartTar(archivePath, innerRel string) (string, []string, bool) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", nil, false
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, false
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var body string
	var helpers []string
	found := false
	for count := 0; count < maxTarEntries; count++ {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		_, afterRoot, cut := strings.Cut(clean, "/")
		if !cut || afterRoot == "" {
			continue
		}
		isTarget := afterRoot == innerRel
		isHelper := strings.HasPrefix(afterRoot, "templates/") && strings.HasSuffix(strings.ToLower(afterRoot), ".tpl")
		if !isTarget && !isHelper {
			continue
		}
		buf, rErr := io.ReadAll(io.LimitReader(tr, repairMaxFileBytes+1))
		if rErr != nil || len(buf) > repairMaxFileBytes {
			continue
		}
		if isTarget {
			body = string(buf)
			found = true
		}
		if isHelper {
			helpers = append(helpers, string(buf))
		}
	}
	return body, helpers, found
}
