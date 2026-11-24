package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/stageproof/stageproof/internal/k8sdiff"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

// ProofRequest is one values-proof pass. Base and Head carry the Helm flags
// for each chart. ValuesFiles are the user's files, not the chart defaults.
type ProofRequest struct {
	BaseDir     string
	HeadDir     string
	ValuesFiles []string
	Base        RenderOptions
	Head        RenderOptions
	Full        bool
}

// ProofSummary is the check line for the values stage.
type ProofSummary struct {
	Keys        int
	ByRender    int
	ByTemplates int
	NotProven   int
}

func (s ProofSummary) String() string {
	text := fmt.Sprintf("%d keys: %d proven by rendering, %d read in templates", s.Keys, s.ByRender, s.ByTemplates)
	if s.NotProven > 0 {
		text += fmt.Sprintf(", %d not proven", s.NotProven)
	}
	return text
}

type leafJob struct {
	file string
	segs []string
}

type keyResult struct {
	findings    []verdict.Finding
	byRender    bool
	byTemplates bool
	notProven   bool
}

// ProveValues decides which user keys still affect the rendered objects.
// Keys the template reader can name exactly stay on that answer unless Full
// is set. Everything else is rendered twice, with volatile fields masked.
func ProveValues(ctx context.Context, req ProofRequest) ([]verdict.Finding, ProofSummary, error) {
	if req.Head.ChartDir == "" {
		req.Head.ChartDir = req.HeadDir
	}
	if req.Base.ChartDir == "" {
		req.Base.ChartDir = req.BaseDir
	}
	if len(req.ValuesFiles) == 0 {
		if !req.Full || req.HeadDir == "" {
			return nil, ProofSummary{}, nil
		}
		findings, err := proveDefaults(ctx, req)
		return findings, ProofSummary{}, err
	}

	templateFindings, _, headUsage, err := DiffValuesConsumption(req.BaseDir, req.HeadDir, req.ValuesFiles)
	if err != nil {
		return nil, ProofSummary{}, err
	}
	byPath := map[string]verdict.Finding{}
	for _, f := range templateFindings {
		byPath[f.File+"\x00"+f.Resource] = f
	}

	var jobs []leafJob
	for _, file := range req.ValuesFiles {
		leaves, _, lErr := ExtractUserValueLeaves(file)
		if lErr != nil {
			return nil, ProofSummary{}, lErr
		}
		seen := map[string]bool{}
		for _, segs := range leaves {
			key := joinSegs(segs)
			if seen[key] {
				continue
			}
			seen[key] = true
			jobs = append(jobs, leafJob{file: file, segs: segs})
		}
	}

	p, err := newProver(ctx)
	if err != nil {
		return nil, ProofSummary{}, err
	}
	defer p.cleanup()

	headBase, headMask, headErr := p.calibrate(req.Head)
	var baseObjs []k8sschema.ManifestObject
	var baseMask map[string]bool
	var baseErr error
	if req.BaseDir != "" {
		baseObjs, baseMask, baseErr = p.calibrate(req.Base)
	}

	results := make([]keyResult, len(jobs))
	workers := proofWorkers()
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, job leafJob) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = p.proveKey(req, job, headUsage, byPath, headBase, headMask, headErr, baseObjs, baseMask, baseErr)
		}(i, job)
	}
	wg.Wait()

	var findings []verdict.Finding
	var sum ProofSummary
	sum.Keys = len(jobs)
	for _, r := range results {
		findings = append(findings, r.findings...)
		if r.byRender {
			sum.ByRender++
		}
		if r.byTemplates {
			sum.ByTemplates++
		}
		if r.notProven {
			sum.NotProven++
		}
	}
	extra, dErr := p.proveDefaults(req, headBase, headMask)
	if dErr != nil {
		return nil, sum, dErr
	}
	findings = append(findings, extra...)
	sortFindings(findings)
	return findings, sum, nil
}

func proofWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		return 8
	}
	if n < 1 {
		return 1
	}
	return n
}

func sortFindings(findings []verdict.Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].RuleID != findings[j].RuleID {
			return findings[i].RuleID < findings[j].RuleID
		}
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Resource != findings[j].Resource {
			return findings[i].Resource < findings[j].Resource
		}
		return findings[i].Message < findings[j].Message
	})
}

type prover struct {
	ctx   context.Context
	tmp   string
	mu    sync.Mutex
	n     int
	cache map[string]renderHit
}

type renderHit struct {
	objs []k8sschema.ManifestObject
	err  error
}

func newProver(ctx context.Context) (*prover, error) {
	tmp, err := os.MkdirTemp("", "stageproof-values-*")
	if err != nil {
		return nil, err
	}
	return &prover{ctx: ctx, tmp: tmp, cache: map[string]renderHit{}}, nil
}

func (p *prover) cleanup() {
	if p.tmp != "" {
		_ = os.RemoveAll(p.tmp)
	}
}

func (p *prover) calibrate(opts RenderOptions) ([]k8sschema.ManifestObject, map[string]bool, error) {
	first, err := p.renderFresh(opts)
	if err != nil {
		return nil, nil, err
	}
	second, err := p.renderFresh(opts)
	if err != nil {
		return nil, nil, err
	}
	p.remember(opts, first, nil)
	return first, k8sdiff.MaskOf(first, second), nil
}

func (p *prover) renderFresh(opts RenderOptions) ([]k8sschema.ManifestObject, error) {
	raw, err := RenderChart(p.ctx, opts)
	if err != nil {
		return nil, err
	}
	return k8sschema.ParseManifestStream(raw)
}

func (p *prover) render(opts RenderOptions) ([]k8sschema.ManifestObject, error) {
	key, err := optsKey(opts)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if hit, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return hit.objs, hit.err
	}
	p.mu.Unlock()
	objs, rErr := p.renderFresh(opts)
	p.remember(opts, objs, rErr)
	return objs, rErr
}

func (p *prover) remember(opts RenderOptions, objs []k8sschema.ManifestObject, err error) {
	key, kErr := optsKey(opts)
	if kErr != nil {
		return
	}
	p.mu.Lock()
	p.cache[key] = renderHit{objs: objs, err: err}
	p.mu.Unlock()
}

func optsKey(opts RenderOptions) (string, error) {
	h := sha256.New()
	if err := hashLine(h, "%s\n%s\n%s\n%t\n%t\n%t\n%s\n", opts.ChartDir, opts.ReleaseName, opts.Namespace, opts.SkipCRDs, opts.IsUpgrade, opts.SkipSchemaValidation, opts.KubeVersion); err != nil {
		return "", err
	}
	for _, s := range opts.APIVersions {
		if err := hashLine(h, "api %s\n", s); err != nil {
			return "", err
		}
	}
	for _, s := range opts.SetValues {
		if err := hashLine(h, "set %s\n", s); err != nil {
			return "", err
		}
	}
	for _, s := range opts.SetStrings {
		if err := hashLine(h, "str %s\n", s); err != nil {
			return "", err
		}
	}
	for _, s := range opts.SetJSON {
		if err := hashLine(h, "json %s\n", s); err != nil {
			return "", err
		}
	}
	for _, s := range opts.SetFiles {
		if err := hashLine(h, "file %s\n", s); err != nil {
			return "", err
		}
	}
	for _, f := range opts.ValuesFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if _, err := h.Write(raw); err != nil {
			return "", err
		}
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashLine(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func (p *prover) proveKey(req ProofRequest, job leafJob, headUsage *ChartUsage, template map[string]verdict.Finding, headBase []k8sschema.ManifestObject, headMask map[string]bool, headErr error, baseObjs []k8sschema.ManifestObject, baseMask map[string]bool, baseErr error) keyResult {
	human := FormatKeyPath(job.segs)
	tmpl, hasTmpl := template[job.file+"\x00"+human]
	if !req.Full && headUsage.Decision(job.segs) == "exact" {
		if hasTmpl {
			tmpl.Proof = "templates"
			return keyResult{findings: []verdict.Finding{tmpl}, byTemplates: true}
		}
		return keyResult{byTemplates: true}
	}
	if headErr != nil || (req.BaseDir != "" && baseErr != nil) {
		return templateFallback(tmpl, hasTmpl)
	}

	head := p.answer(req.Head, job, headBase, headMask)
	if head.failed {
		return templateFallback(tmpl, hasTmpl)
	}
	if head.unknown {
		return keyResult{notProven: true}
	}
	base := effectAnswer{}
	if req.BaseDir != "" {
		base = p.answer(req.Base, job, baseObjs, baseMask)
		if base.failed {
			return templateFallback(tmpl, hasTmpl)
		}
		if base.unknown {
			return keyResult{notProven: true}
		}
	}

	if head.has {
		return keyResult{byRender: true}
	}
	if req.BaseDir != "" && !base.has {
		if hasTmpl && tmpl.RuleID == "helm.values.preexisting_orphan" {
			tmpl.Proof = "render"
			return keyResult{findings: []verdict.Finding{tmpl}, byRender: true}
		}
		return keyResult{byRender: true}
	}
	// When head was rendered with --is-upgrade and base with install mode, a
	// key used only under .Release.IsInstall affects base but not head. Check
	// head in base's mode before concluding the upgrade dropped the key.
	if req.BaseDir != "" && req.Head.IsUpgrade != req.Base.IsUpgrade {
		sameMode := req.Head
		sameMode.IsUpgrade = req.Base.IsUpgrade
		if sameObjs, sameMask, sameErr := p.calibrate(sameMode); sameErr == nil {
			alt := p.answer(sameMode, job, sameObjs, sameMask)
			if alt.has {
				return keyResult{byRender: true}
			}
			if alt.unknown {
				return keyResult{notProven: true}
			}
		}
	}

	finding := tmpl
	if !hasTmpl {
		msg := fmt.Sprintf("Values key `%s` was consumed in baseline chart but is silently ignored in upgraded chart.", human)
		if req.BaseDir == "" {
			msg = fmt.Sprintf("Values key `%s` is not referenced anywhere in chart templates, default values.yaml, or values.schema.json.", human)
		}
		finding = verdict.Finding{
			RuleID:   "helm.values.silently_dropped",
			Severity: verdict.SeverityBlocker,
			File:     job.file,
			Resource: human,
			Message:  msg,
		}
	}
	finding.Proof = "render"
	finding.Detail = ""
	out := []verdict.Finding{finding}
	out = append(out, p.renameFindings(req, job, base.effects, headMask)...)
	return keyResult{findings: out, byRender: true}
}

func templateFallback(tmpl verdict.Finding, ok bool) keyResult {
	if !ok {
		return keyResult{byTemplates: true}
	}
	tmpl.Proof = "templates"
	return keyResult{findings: []verdict.Finding{tmpl}, byTemplates: true}
}

type effectAnswer struct {
	effects []k8sdiff.Effect
	has     bool
	unknown bool
	failed  bool
}

func (p *prover) answer(opts RenderOptions, job leafJob, with []k8sschema.ManifestObject, mask map[string]bool) effectAnswer {
	effects, unknown, failed := p.keyEffect(opts, job, with, mask)
	if failed {
		return effectAnswer{failed: true}
	}
	if unknown {
		return effectAnswer{unknown: true}
	}
	if len(effects) > 0 {
		return effectAnswer{effects: effects, has: true}
	}
	changed, unk, fail := p.probe(opts, job, with, mask)
	if fail {
		return effectAnswer{failed: true}
	}
	if unk {
		return effectAnswer{unknown: true}
	}
	return effectAnswer{has: changed}
}

// keyEffect is the difference between rendering with the key and without it.
// unknown is a schema rejection of the perturbation. failed means rendering
// could not answer and the caller should keep the template result.
func (p *prover) keyEffect(opts RenderOptions, job leafJob, with []k8sschema.ManifestObject, mask map[string]bool) ([]k8sdiff.Effect, bool, bool) {
	withoutFile, err := p.rewrite(job.file, job.segs, "drop", nil)
	if err != nil {
		return nil, false, true
	}
	withoutOpts := opts
	withoutOpts.ValuesFiles = replaceFile(opts.ValuesFiles, job.file, withoutFile)
	without, err := p.render(withoutOpts)
	if err != nil {
		if schemaRejected(err) {
			return nil, true, false
		}
		return nil, false, true
	}
	effects := k8sdiff.Diff(without, with, mask)
	return effects, false, false
}

// probe reports whether a different value of the same type changes the render.
// unknown means the chart schema rejected that value, which is not a block.
func (p *prover) probe(opts RenderOptions, job leafJob, with []k8sschema.ManifestObject, mask map[string]bool) (bool, bool, bool) {
	path, err := p.rewrite(job.file, job.segs, "probe", nil)
	if err != nil {
		return false, false, true
	}
	probe := opts
	probe.ValuesFiles = replaceFile(opts.ValuesFiles, job.file, path)
	objs, err := p.render(probe)
	if err != nil {
		if schemaRejected(err) {
			return false, true, false
		}
		return false, false, true
	}
	return !k8sdiff.Same(with, objs, mask), false, false
}

func (p *prover) renameFindings(req ProofRequest, job leafJob, baseEffects []k8sdiff.Effect, headMask map[string]bool) []verdict.Finding {
	if len(baseEffects) == 0 || req.BaseDir == "" {
		return nil
	}
	cands := RankValueKeys(job.segs, newChartLeaves(req.BaseDir, req.HeadDir))
	if len(cands) == 0 {
		return nil
	}
	leaf, err := leafNode(job.file, job.segs)
	if err != nil {
		return nil
	}
	human := FormatKeyPath(job.segs)
	var out []verdict.Finding
	verified := false
	var matched string
	for _, cand := range cands {
		withFile, wErr := p.rewrite(job.file, cand, "set", leaf)
		if wErr != nil {
			continue
		}
		withoutFile, dErr := p.rewrite(job.file, cand, "drop", nil)
		if dErr != nil {
			withoutFile = job.file
		}
		withOpts := req.Head
		withOpts.ValuesFiles = replaceFile(req.Head.ValuesFiles, job.file, withFile)
		withoutOpts := req.Head
		withoutOpts.ValuesFiles = replaceFile(req.Head.ValuesFiles, job.file, withoutFile)
		withObjs, wRender := p.render(withOpts)
		withoutObjs, dRender := p.render(withoutOpts)
		if wRender != nil || dRender != nil {
			continue
		}
		effects := k8sdiff.Diff(withoutObjs, withObjs, headMask)
		if sameEffectList(baseEffects, effects) {
			verified = true
			matched = FormatKeyPath(cand)
			break
		}
		if matched == "" {
			matched = FormatKeyPath(cand)
		}
	}
	if matched == "" {
		return nil
	}
	flag := verified
	msg := fmt.Sprintf("Values key `%s` is not a verified rename of `%s`.", human, matched)
	if verified {
		msg = fmt.Sprintf("Values key `%s` has the same rendered effect as `%s`.", human, matched)
	}
	out = append(out, verdict.Finding{
		RuleID:   "helm.values.renamed",
		Severity: verdict.SeverityWarning,
		File:     job.file,
		Resource: human,
		Message:  msg,
		Proof:    "render",
		Verified: &flag,
	})
	return out
}

func sameEffectList(a, b []k8sdiff.Effect) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Object != b[i].Object || a[i].Path != b[i].Path || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func replaceFile(files []string, old, repl string) []string {
	out := append([]string(nil), files...)
	if len(out) == 0 {
		return []string{repl}
	}
	found := false
	for i, f := range out {
		if f == old {
			out[i] = repl
			found = true
		}
	}
	if !found {
		out = append(out, repl)
	}
	return out
}

func (p *prover) rewrite(file string, segs []string, op string, set *yaml.Node) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	switch op {
	case "drop":
		if err := dropLeaf(&doc, segs); err != nil {
			return "", err
		}
	case "probe":
		if err := probeLeaf(&doc, segs); err != nil {
			return "", err
		}
	case "set":
		if err := setLeaf(&doc, segs, set); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unknown values edit %s", op)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.n++
	name := filepath.Join(p.tmp, fmt.Sprintf("values-%d.yaml", p.n))
	p.mu.Unlock()
	if err := os.WriteFile(name, out, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

func docMap(doc *yaml.Node) *yaml.Node {
	if doc != nil && doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func dropLeaf(doc *yaml.Node, segs []string) error {
	parent, _, err := parentOf(doc, segs)
	if err != nil {
		return err
	}
	key := segs[len(segs)-1]
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return nil
		}
	}
	return fmt.Errorf("values key %s was not in the file", FormatKeyPath(segs))
}

func probeLeaf(doc *yaml.Node, segs []string) error {
	_, leaf, err := parentOf(doc, segs)
	if err != nil {
		return err
	}
	switch leaf.Kind {
	case yaml.SequenceNode:
		leaf.Content = append(leaf.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "stageproof-probe"})
	case yaml.MappingNode:
		leaf.Content = append(leaf.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "stageproof-probe"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "1"},
		)
	default:
		switch leaf.Tag {
		case "!!bool":
			if leaf.Value == "true" {
				leaf.Value = "false"
			} else {
				leaf.Value = "true"
			}
		case "!!int":
			var n int
			if _, err := fmt.Sscan(leaf.Value, &n); err != nil {
				leaf.Tag = "!!str"
				leaf.Value += "-stageproof-probe"
				break
			}
			leaf.Value = fmt.Sprintf("%d", n+1)
		case "!!float":
			var n float64
			if _, err := fmt.Sscan(leaf.Value, &n); err != nil {
				leaf.Tag = "!!str"
				leaf.Value += "-stageproof-probe"
				break
			}
			leaf.Value = fmt.Sprintf("%v", n+1)
		default:
			leaf.Tag = "!!str"
			leaf.Value += "-stageproof-probe"
		}
	}
	return nil
}

func setLeaf(doc *yaml.Node, segs []string, src *yaml.Node) error {
	if src == nil || len(segs) == 0 {
		return fmt.Errorf("no value to set")
	}
	root := docMap(doc)
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("values file is not a map")
	}
	cur := root
	for i, seg := range segs {
		idx := -1
		for j := 0; j+1 < len(cur.Content); j += 2 {
			if cur.Content[j].Value == seg {
				idx = j
				break
			}
		}
		last := i == len(segs)-1
		if last {
			clone := *src
			if idx >= 0 {
				cur.Content[idx+1] = &clone
			} else {
				cur.Content = append(cur.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: seg},
					&clone,
				)
			}
			return nil
		}
		if idx < 0 {
			child := &yaml.Node{Kind: yaml.MappingNode}
			cur.Content = append(cur.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: seg},
				child,
			)
			cur = child
			continue
		}
		next := cur.Content[idx+1]
		if next.Kind != yaml.MappingNode {
			next = &yaml.Node{Kind: yaml.MappingNode}
			cur.Content[idx+1] = next
		}
		cur = next
	}
	return nil
}

func parentOf(doc *yaml.Node, segs []string) (*yaml.Node, *yaml.Node, error) {
	if len(segs) == 0 {
		return nil, nil, fmt.Errorf("empty values key")
	}
	cur := docMap(doc)
	for i, seg := range segs {
		if cur == nil || cur.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("values key %s is not a map path", FormatKeyPath(segs))
		}
		var child *yaml.Node
		for j := 0; j+1 < len(cur.Content); j += 2 {
			if cur.Content[j].Value == seg {
				child = cur.Content[j+1]
				break
			}
		}
		if child == nil {
			return nil, nil, fmt.Errorf("values key %s was not in the file", FormatKeyPath(segs))
		}
		if i == len(segs)-1 {
			return cur, child, nil
		}
		cur = child
	}
	return nil, nil, fmt.Errorf("values key %s was not in the file", FormatKeyPath(segs))
}

func leafNode(file string, segs []string) (*yaml.Node, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	_, leaf, err := parentOf(&doc, segs)
	if err != nil {
		return nil, err
	}
	clone := *leaf
	return &clone, nil
}

func schemaRejected(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "schema")
}

func newChartLeaves(baseDir, headDir string) [][]string {
	old := chartLeafSet(baseDir)
	var out [][]string
	seen := map[string]bool{}
	for _, segs := range chartLeafList(headDir) {
		key := joinSegs(segs)
		if old[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, segs)
	}
	sort.Slice(out, func(i, j int) bool { return joinSegs(out[i]) < joinSegs(out[j]) })
	return out
}

func chartLeafSet(dir string) map[string]bool {
	out := map[string]bool{}
	for _, segs := range chartLeafList(dir) {
		out[joinSegs(segs)] = true
	}
	return out
}

func chartLeafList(dir string) [][]string {
	if dir == "" {
		return nil
	}
	var out [][]string
	if leaves, _, err := ExtractUserValueLeaves(filepath.Join(dir, "values.yaml")); err == nil {
		out = append(out, leaves...)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "values.schema.json"))
	if err != nil {
		return out
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return out
	}
	var segs [][]string
	collectSchemaLeaves(schema, nil, &segs)
	out = append(out, segs...)
	return out
}

func collectSchemaLeaves(schema map[string]any, prefix []string, out *[][]string) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		if len(prefix) > 0 {
			*out = append(*out, append([]string(nil), prefix...))
		}
		return
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		next := append(append([]string(nil), prefix...), k)
		sub, _ := props[k].(map[string]any)
		if sub != nil {
			if _, nested := sub["properties"]; nested {
				collectSchemaLeaves(sub, next, out)
				continue
			}
		}
		*out = append(*out, next)
	}
}

func (p *prover) proveDefaults(req ProofRequest, headBase []k8sschema.ManifestObject, headMask map[string]bool) ([]verdict.Finding, error) {
	if req.BaseDir == "" || req.HeadDir == "" {
		return nil, nil
	}
	if len(req.ValuesFiles) == 0 && !req.Full {
		return nil, nil
	}
	changed, err := changedDefaults(req.BaseDir, req.HeadDir, req.ValuesFiles)
	if err != nil || len(changed) == 0 {
		return nil, err
	}
	if headBase == nil {
		var cErr error
		headBase, headMask, cErr = p.calibrate(req.Head)
		if cErr != nil {
			return nil, nil
		}
	}
	if !req.Full && len(changed) > 8 {
		return p.combinedDefault(req, changed, headBase, headMask), nil
	}
	var out []verdict.Finding
	for _, ch := range changed {
		overlay, oErr := p.overlay(map[string]*yaml.Node{joinSegs(ch.segs): ch.oldNode}, [][]string{ch.segs})
		if oErr != nil {
			return nil, oErr
		}
		opts := req.Head
		opts.ValuesFiles = append(append([]string{}, req.Head.ValuesFiles...), overlay)
		objs, rErr := p.render(opts)
		if rErr != nil {
			continue
		}
		effects := k8sdiff.Diff(headBase, objs, headMask)
		if len(effects) == 0 {
			continue
		}
		out = append(out, defaultFinding(ch, effects))
	}
	return out, nil
}

func proveDefaults(ctx context.Context, req ProofRequest) ([]verdict.Finding, error) {
	p, err := newProver(ctx)
	if err != nil {
		return nil, err
	}
	defer p.cleanup()
	return p.proveDefaults(req, nil, nil)
}

type defaultChange struct {
	segs    []string
	oldShow string
	newShow string
	oldNode *yaml.Node
}

func changedDefaults(baseDir, headDir string, userFiles []string) ([]defaultChange, error) {
	old, err := defaultLeaves(filepath.Join(baseDir, "values.yaml"))
	if err != nil {
		return nil, err
	}
	neu, err := defaultLeaves(filepath.Join(headDir, "values.yaml"))
	if err != nil {
		return nil, err
	}
	user := map[string]bool{}
	for _, file := range userFiles {
		leaves, _, lErr := ExtractUserValueLeaves(file)
		if lErr != nil {
			return nil, lErr
		}
		for _, segs := range leaves {
			user[joinSegs(segs)] = true
		}
	}
	keys := make([]string, 0, len(old))
	for k := range old {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []defaultChange
	for _, k := range keys {
		if userCovers(user, splitSegs(k)) {
			continue
		}
		o := old[k]
		n, ok := neu[k]
		if !ok {
			out = append(out, defaultChange{segs: o.segs, oldShow: o.show, newShow: "unset", oldNode: o.node})
			continue
		}
		if o.canon == n.canon {
			continue
		}
		out = append(out, defaultChange{segs: o.segs, oldShow: o.show, newShow: n.show, oldNode: o.node})
	}
	return out, nil
}

func userCovers(user map[string]bool, segs []string) bool {
	for i := 1; i <= len(segs); i++ {
		if user[joinSegs(segs[:i])] {
			return true
		}
	}
	return false
}

type storedLeaf struct {
	segs  []string
	canon string
	show  string
	node  *yaml.Node
}

func defaultLeaves(path string) (map[string]storedLeaf, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]storedLeaf{}, nil
		}
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
	}
	out := map[string]storedLeaf{}
	var walk func(n *yaml.Node, prefix []string)
	walk = func(n *yaml.Node, prefix []string) {
		if n == nil {
			return
		}
		if n.Kind == yaml.DocumentNode {
			for _, c := range n.Content {
				walk(c, prefix)
			}
			return
		}
		if n.Kind == yaml.MappingNode && len(n.Content) > 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				next := append(append([]string(nil), prefix...), n.Content[i].Value)
				walk(n.Content[i+1], next)
			}
			return
		}
		if len(prefix) == 0 {
			return
		}
		clone := *n
		canonRaw, mErr := yaml.Marshal(&clone)
		canon := string(canonRaw)
		if mErr != nil {
			canon = n.Value
		}
		show := ""
		if n.Kind == yaml.ScalarNode && len(n.Value) <= 80 && !strings.Contains(n.Value, "\n") {
			show = n.Value
		}
		out[joinSegs(prefix)] = storedLeaf{segs: append([]string(nil), prefix...), canon: canon, show: show, node: &clone}
	}
	walk(&doc, nil)
	return out, nil
}

func (p *prover) overlay(nodes map[string]*yaml.Node, order [][]string) (string, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	for _, segs := range order {
		if err := setLeaf(doc, segs, nodes[joinSegs(segs)]); err != nil {
			return "", err
		}
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.n++
	name := filepath.Join(p.tmp, fmt.Sprintf("defaults-%d.yaml", p.n))
	p.mu.Unlock()
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

func (p *prover) combinedDefault(req ProofRequest, changed []defaultChange, headBase []k8sschema.ManifestObject, headMask map[string]bool) []verdict.Finding {
	nodes := map[string]*yaml.Node{}
	var order [][]string
	for _, ch := range changed {
		nodes[joinSegs(ch.segs)] = ch.oldNode
		order = append(order, ch.segs)
	}
	overlay, err := p.overlay(nodes, order)
	if err != nil {
		return nil
	}
	opts := req.Head
	opts.ValuesFiles = append(append([]string{}, req.Head.ValuesFiles...), overlay)
	objs, rErr := p.render(opts)
	if rErr != nil || k8sdiff.Same(headBase, objs, headMask) {
		return nil
	}
	names := make([]string, 0, len(changed))
	for _, ch := range changed {
		names = append(names, FormatKeyPath(ch.segs))
	}
	sort.Strings(names)
	detail := "Keys: " + strings.Join(names, ", ")
	if len(names) > 20 {
		detail = fmt.Sprintf("Keys: %s, and %d more", strings.Join(names[:20], ", "), len(names)-20)
	}
	detail += ". Re-run with --values-proof full to prove each key."
	return []verdict.Finding{{
		RuleID:   "helm.values.default_changed",
		Severity: verdict.SeverityWarning,
		Message:  "Chart defaults changed and the rendered objects differ.",
		Detail:   detail,
		Proof:    "render",
	}}
}

func defaultFinding(ch defaultChange, effects []k8sdiff.Effect) verdict.Finding {
	path := FormatKeyPath(ch.segs)
	msg := fmt.Sprintf("Chart default `%s` changed and the rendered objects differ.", path)
	if ch.oldShow != "" && ch.newShow != "" {
		msg = fmt.Sprintf("Chart default `%s` changed from `%s` to `%s` and the rendered objects differ.", path, ch.oldShow, ch.newShow)
	}
	return verdict.Finding{
		RuleID:   "helm.values.default_changed",
		Severity: verdict.SeverityWarning,
		Resource: path,
		Message:  msg,
		Detail:   "Affected objects: " + objectList(effects),
		Proof:    "render",
	}
}

func objectList(effects []k8sdiff.Effect) string {
	seen := map[string]bool{}
	var names []string
	for _, e := range effects {
		if e.Object == "" || seen[e.Object] {
			continue
		}
		seen[e.Object] = true
		names = append(names, e.Object)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// RequiredFailure reports a Helm `required` failure with user values removed.
func RequiredFailure(err error, secrets []string) (string, bool) {
	if err == nil {
		return "", false
	}
	text := err.Error()
	low := strings.ToLower(text)
	if !strings.Contains(low, "execution error") || !strings.Contains(low, "required") {
		return "", false
	}
	return scrubSecrets(text, secrets), true
}

// SecretStrings collects scalar strings from values files so a Helm error
// can be scrubbed. Values inside a list are included. Short tokens are left alone.
func SecretStrings(files []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, file := range files {
		raw, rErr := os.ReadFile(file)
		if rErr != nil {
			continue
		}
		var doc yaml.Node
		if yaml.Unmarshal(raw, &doc) != nil {
			continue
		}
		collectValueScalars(&doc, seen, &out)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// SecretValuesFromSets returns the value after the first '=' in each Helm
// assignment. key=val=more yields val=more. Short tokens are left alone.
func SecretValuesFromSets(sets []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range sets {
		_, val, ok := strings.Cut(raw, "=")
		if !ok || len(val) < 4 || seen[val] {
			continue
		}
		seen[val] = true
		out = append(out, val)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Scrub replaces secret strings in a Helm error. Longer strings are replaced first.
func Scrub(msg string, secrets []string) string {
	ordered := append([]string{}, secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	return scrubSecrets(msg, ordered)
}

func collectValueScalars(n *yaml.Node, seen map[string]bool, out *[]string) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			collectValueScalars(child, seen, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			collectValueScalars(n.Content[i+1], seen, out)
		}
	case yaml.AliasNode:
		collectValueScalars(n.Alias, seen, out)
	case yaml.ScalarNode:
		if len(n.Value) < 4 || seen[n.Value] {
			return
		}
		seen[n.Value] = true
		*out = append(*out, n.Value)
	}
}

func scrubSecrets(msg string, secrets []string) string {
	for _, s := range secrets {
		if len(s) < 4 {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	return msg
}
