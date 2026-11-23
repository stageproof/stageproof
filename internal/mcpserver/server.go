// Package mcpserver serves Stageproof over stdio MCP. Tools read files only
// inside --root. They do not open a port, write a report file, or start a
// live rehearsal.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stageproof/stageproof/internal/adapter"
	"github.com/stageproof/stageproof/internal/argocd"
	"github.com/stageproof/stageproof/internal/check"
	"github.com/stageproof/stageproof/internal/facts"
	"github.com/stageproof/stageproof/internal/flux"
	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/rules"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Run serves until the client closes stdin. Logs go to stderr.
func Run(ctx context.Context, root string) error {
	srv, err := New(root, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// New builds a server whose tools cannot read outside root.
func New(root string, logger *slog.Logger) (*mcp.Server, error) {
	abs, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	box := &box{root: abs}
	srv := mcp.NewServer(&mcp.Implementation{Name: "stageproof", Version: "dev"}, &mcp.ServerOptions{Logger: logger})
	mcp.AddTool(srv, &mcp.Tool{Name: "check", Description: "Compare two charts or two rendered manifest trees. Paths stay inside --root. Live rehearsal and report files are off."}, box.check)
	mcp.AddTool(srv, &mcp.Tool{Name: "check_pr", Description: "Check charts changed between two git refs in --root. Live rehearsal and report files are off."}, box.checkPR)
	mcp.AddTool(srv, &mcp.Tool{Name: "explain", Description: "Return the level, title, meaning, and fix for one rule id."}, box.explain)
	return srv, nil
}

type box struct {
	root string
}

func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("--root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("--root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--root is not a directory")
	}
	return resolved, nil
}

func (b *box) confine(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("%s is outside --root", p)
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside --root", p)
	}
	joined := filepath.Join(b.root, clean)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	rel, err := filepath.Rel(b.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s is outside --root", p)
	}
	return resolved, nil
}

// allow accepts a path this process produced. It must sit under --root or
// under the temp directory, where chart unpacks are written.
func (b *box) allow(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if !filepath.IsAbs(p) {
		return b.confine(p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if under(b.root, resolved) {
		return resolved, nil
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err == nil && under(tmp, resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("resolved path is outside --root")
}

func (b *box) allowList(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := b.allow(p)
		if err != nil {
			return nil, err
		}
		if abs != "" {
			out = append(out, abs)
		}
	}
	return out, nil
}

func under(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func (b *box) confineList(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := b.confine(p)
		if err != nil {
			return nil, err
		}
		if abs != "" {
			out = append(out, abs)
		}
	}
	return out, nil
}

type checkIn struct {
	CurrentChart string   `json:"current_chart,omitempty" jsonschema:"chart directory you run today, relative to --root"`
	NewChart     string   `json:"new_chart,omitempty" jsonschema:"chart directory to deploy, relative to --root"`
	Values       []string `json:"values,omitempty" jsonschema:"values files relative to --root"`
	BaseValues   []string `json:"base_values,omitempty" jsonschema:"values files for the current chart"`
	ArgoCDFile   string   `json:"argocd_file,omitempty" jsonschema:"Argo CD Application file relative to --root"`
	FluxFile     string   `json:"flux_file,omitempty" jsonschema:"Flux HelmRelease file relative to --root"`
	Before       string   `json:"before,omitempty" jsonschema:"rendered manifests before the change"`
	After        string   `json:"after,omitempty" jsonschema:"rendered manifests after the change"`
	ClusterFacts string   `json:"cluster_facts,omitempty" jsonschema:"JSON from stageproof facts export"`
	APIServerDir string   `json:"api_server_dir,omitempty" jsonschema:"directory of kube-apiserver, etcd, and kubectl"`
	CRDFile      []string `json:"crd_file,omitempty" jsonschema:"extra CRD files relative to --root"`
	CurrentK8s   string   `json:"current_k8s,omitempty" jsonschema:"Kubernetes minor, default 1.33"`
	NextK8s      string   `json:"next_k8s,omitempty"`
	APIServer    string   `json:"api_server,omitempty" jsonschema:"auto, off, or require"`
	ValuesProof  string   `json:"values_proof,omitempty" jsonschema:"default or full"`
	Offline      bool     `json:"offline,omitempty"`
	Live         *bool    `json:"live,omitempty"`
	Out          *string  `json:"out,omitempty"`
	CommentFile  *string  `json:"comment_file,omitempty"`
}

func (b *box) check(ctx context.Context, _ *mcp.CallToolRequest, in checkIn) (*mcp.CallToolResult, map[string]any, error) {
	if err := rejectWrites(in.Live, in.Out, in.CommentFile); err != nil {
		return nil, nil, err
	}
	current, err := b.confine(in.CurrentChart)
	if err != nil {
		return nil, nil, err
	}
	head, err := b.confine(in.NewChart)
	if err != nil {
		return nil, nil, err
	}
	before, err := b.confine(in.Before)
	if err != nil {
		return nil, nil, err
	}
	after, err := b.confine(in.After)
	if err != nil {
		return nil, nil, err
	}
	argoFile, err := b.confine(in.ArgoCDFile)
	if err != nil {
		return nil, nil, err
	}
	fluxFile, err := b.confine(in.FluxFile)
	if err != nil {
		return nil, nil, err
	}
	factsPath, err := b.confine(in.ClusterFacts)
	if err != nil {
		return nil, nil, err
	}
	apiDir, err := b.confine(in.APIServerDir)
	if err != nil {
		return nil, nil, err
	}
	values, err := b.confineList(in.Values)
	if err != nil {
		return nil, nil, err
	}
	baseValues, err := b.confineList(in.BaseValues)
	if err != nil {
		return nil, nil, err
	}
	crds, err := b.confineList(in.CRDFile)
	if err != nil {
		return nil, nil, err
	}
	mode, err := checkMode(in.APIServer, in.ValuesProof)
	if err != nil {
		return nil, nil, err
	}
	k8s := in.CurrentK8s
	if strings.TrimSpace(k8s) == "" {
		k8s = "1.33"
	}
	doc, err := loadFacts(factsPath)
	if err != nil {
		return nil, nil, err
	}

	input := check.Input{
		BaseDir:        current,
		HeadDir:        head,
		BaseRef:        relLabel(b.root, current),
		HeadRef:        relLabel(b.root, head),
		BaseValues:     baseValues,
		HeadValues:     values,
		KubeVersion:    k8s,
		NextKubernetes: strings.TrimSpace(in.NextK8s),
		APIServer:      mode.api,
		APIServerDir:   apiDir,
		ValuesFull:     mode.full,
		Offline:        in.Offline,
		CRDFiles:       crds,
		Facts:          doc,
		Started:        time.Now(),
	}
	if before != "" || after != "" {
		if before == "" || after == "" {
			return nil, nil, fmt.Errorf("before and after are both required")
		}
		if head != "" || current != "" || argoFile != "" || fluxFile != "" {
			return nil, nil, fmt.Errorf("before and after cannot be combined with a chart, an Application, or a HelmRelease")
		}
		if len(values) > 0 || len(baseValues) > 0 {
			return nil, nil, fmt.Errorf("values files are for Helm charts")
		}
		input.Adapter = "manifests"
		input.ManifestMode = true
		input.ManifestBefore = before
		input.ManifestAfter = after
		input.BaseRef = relLabel(b.root, before)
		input.HeadRef = relLabel(b.root, after)
		input.BaseDir = ""
		input.HeadDir = ""
	} else if argoFile != "" || fluxFile != "" {
		if argoFile != "" && fluxFile != "" {
			return nil, nil, fmt.Errorf("argocd_file and flux_file cannot be combined")
		}
		if head != "" || current != "" {
			return nil, nil, fmt.Errorf("a chart directory cannot be combined with an Application or a HelmRelease")
		}
		rep, err := b.gitOps(ctx, argoFile, fluxFile, input)
		if err != nil {
			return nil, nil, err
		}
		out, err := reportMap(rep)
		return nil, out, err
	} else if head == "" {
		return nil, nil, fmt.Errorf("new_chart, argocd_file, flux_file, or before and after is required")
	}
	if len(input.BaseValues) == 0 {
		input.BaseValues = input.HeadValues
	}
	rep := check.Run(ctx, input)
	out, err := reportMap(rep)
	return nil, out, err
}

func (b *box) gitOps(ctx context.Context, argoFile, fluxFile string, input check.Input) (*verdict.Report, error) {
	if argoFile != "" {
		spec, _, err := argocd.ParseApplicationFile(argoFile, nil)
		if err != nil {
			return nil, err
		}
		mat, err := argocd.Materialize(ctx, spec, argocd.MaterializeOptions{RepoRoot: b.root})
		if err != nil {
			return nil, err
		}
		defer mat.Cleanup()
		input.Adapter = "argocd"
		input.HeadDir = mat.ChartDir
		input.HeadRef = relLabel(b.root, argoFile)
		input.BaseRef = input.HeadRef
		input.HeadValues = append(input.HeadValues, mat.ValueFiles...)
		input.BaseValues = input.HeadValues
		input.HeadSet = mat.SetValues
		input.HeadSetString = mat.SetStrings
		input.HeadSetFile = mat.SetFiles
		input.HeadRelease = mat.ReleaseName
		input.HeadNamespace = mat.Namespace
		input.ArgoSkipCrds = mat.SkipCrds
		return check.Run(ctx, input), nil
	}
	rel, _, err := flux.ParseFile(fluxFile, nil)
	if err != nil {
		return nil, err
	}
	mat, err := flux.Materialize(ctx, rel, flux.MaterializeOptions{RepoRoot: b.root})
	if err != nil {
		return nil, err
	}
	defer mat.Cleanup()
	input.Adapter = "flux"
	input.HeadDir = mat.ChartDir
	input.HeadRef = relLabel(b.root, fluxFile)
	input.BaseRef = input.HeadRef
	input.HeadValues = append(input.HeadValues, mat.ValueFiles...)
	input.BaseValues = input.HeadValues
	input.HeadRelease = mat.ReleaseName
	input.HeadNamespace = mat.Namespace
	input.FluxCRDPolicy = mat.CRDPolicy
	input.ValueGaps = mat.Gaps
	return check.Run(ctx, input), nil
}

type prIn struct {
	CurrentGit   string  `json:"current_git,omitempty" jsonschema:"base git ref, default origin/main"`
	NewGit       string  `json:"new_git,omitempty" jsonschema:"head git ref, default HEAD"`
	CurrentK8s   string  `json:"current_k8s,omitempty"`
	NextK8s      string  `json:"next_k8s,omitempty"`
	APIServer    string  `json:"api_server,omitempty"`
	APIServerDir string  `json:"api_server_dir,omitempty"`
	ClusterFacts string  `json:"cluster_facts,omitempty"`
	ValuesProof  string  `json:"values_proof,omitempty"`
	Offline      bool    `json:"offline,omitempty"`
	Live         *bool   `json:"live,omitempty"`
	Out          *string `json:"out,omitempty"`
	CommentFile  *string `json:"comment_file,omitempty"`
}

func (b *box) checkPR(ctx context.Context, _ *mcp.CallToolRequest, in prIn) (*mcp.CallToolResult, map[string]any, error) {
	if err := rejectWrites(in.Live, in.Out, in.CommentFile); err != nil {
		return nil, nil, err
	}
	baseRef := strings.TrimSpace(in.CurrentGit)
	if baseRef == "" {
		baseRef = "origin/main"
	}
	headRef := strings.TrimSpace(in.NewGit)
	if headRef == "" {
		headRef = "HEAD"
	}
	if !safeRef(baseRef) || !safeRef(headRef) {
		return nil, nil, fmt.Errorf("git ref is empty or starts with a dash")
	}
	mode, err := checkMode(in.APIServer, in.ValuesProof)
	if err != nil {
		return nil, nil, err
	}
	apiDir, err := b.confine(in.APIServerDir)
	if err != nil {
		return nil, nil, err
	}
	factsPath, err := b.confine(in.ClusterFacts)
	if err != nil {
		return nil, nil, err
	}
	doc, err := loadFacts(factsPath)
	if err != nil {
		return nil, nil, err
	}
	names, err := gitDiff(ctx, b.root, baseRef, headRef)
	if err != nil {
		return nil, nil, err
	}
	set, err := adapter.New(b.root)
	if err != nil {
		return nil, nil, err
	}
	targets := set.Discover(b.root, names)
	k8s := in.CurrentK8s
	if strings.TrimSpace(k8s) == "" {
		k8s = "1.33"
	}
	sha := headSHA(ctx, b.root, headRef)
	if len(targets) == 0 {
		out, err := combinedMap(&report.Combined{Verdict: verdict.Passed, ExitCode: 0, HeadGit: sha, Reports: []*verdict.Report{}})
		return nil, out, err
	}
	var reports []*verdict.Report
	worst := verdict.Proven
	for _, target := range targets {
		rel, err := filepath.Rel(b.root, filepath.Join(b.root, target.Path))
		if err != nil {
			return nil, nil, err
		}
		abs, err := b.confine(rel)
		if err != nil {
			return nil, nil, err
		}
		target.Path = abs
		resolved, err := set.Resolve(ctx, target, baseRef)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		if resolved.Cleanup != nil {
			defer resolved.Cleanup()
		}
		reps, err := b.runTarget(ctx, resolved, rel, baseRef, check.Input{
			KubeVersion:    k8s,
			NextKubernetes: strings.TrimSpace(in.NextK8s),
			APIServer:      mode.api,
			APIServerDir:   apiDir,
			ValuesFull:     mode.full,
			Offline:        in.Offline,
			Facts:          doc,
			Started:        time.Now(),
		})
		if err != nil {
			return nil, nil, err
		}
		for _, rep := range reps {
			if rep.HeadGit == "" {
				rep.HeadGit = sha
			}
			reports = append(reports, rep)
			if rep.Verdict.Rank() > worst.Rank() {
				worst = rep.Verdict
			}
		}
	}
	out, err := combinedMap(&report.Combined{Verdict: worst, ExitCode: worst.ExitCode(), HeadGit: sha, Reports: reports})
	return nil, out, err
}

func (b *box) runTarget(ctx context.Context, resolved adapter.ResolvedTarget, rel, baseRef string, input check.Input) ([]*verdict.Report, error) {
	switch resolved.Adapter {
	case "argocd":
		rep, err := b.gitOps(ctx, resolved.ArgoFile, "", input)
		if err != nil {
			return nil, err
		}
		return []*verdict.Report{rep}, nil
	case "flux":
		rep, err := b.gitOps(ctx, "", resolved.FluxFile, input)
		if err != nil {
			return nil, err
		}
		return []*verdict.Report{rep}, nil
	default:
		head, err := b.allow(resolved.HeadChart)
		if err != nil {
			return nil, err
		}
		base, cleanup, err := archiveChart(ctx, b.root, baseRef, rel)
		if err != nil {
			return nil, fmt.Errorf("git archive %s %s failed", baseRef, rel)
		}
		if cleanup != nil {
			defer cleanup()
		}
		values, err := b.allowList(resolved.Values)
		if err != nil {
			return nil, err
		}
		input.Adapter = "helm"
		input.HeadDir = head
		input.BaseDir = base
		input.HeadRef = rel
		input.BaseRef = baseRef + ":" + rel
		if len(values) == 0 {
			return []*verdict.Report{check.Run(ctx, input)}, nil
		}
		// Each values file is one deployment, matching check-pr.
		out := make([]*verdict.Report, 0, len(values))
		for _, vf := range values {
			one := input
			one.HeadValues = []string{vf}
			one.BaseValues = []string{vf}
			out = append(out, check.Run(ctx, one))
		}
		return out, nil
	}
}

func headSHA(ctx context.Context, root, ref string) string {
	out, err := gitOutput(ctx, root, "rev-parse", "--verify", ref)
	if err != nil {
		return ref
	}
	return strings.TrimSpace(string(out))
}

type explainIn struct {
	RuleID string `json:"rule_id" jsonschema:"rule id, for example helm.values.silently_dropped"`
}

type explainOut struct {
	ID      string `json:"id"`
	Level   string `json:"level"`
	Title   string `json:"title"`
	Meaning string `json:"meaning"`
	Fix     string `json:"fix"`
}

func (b *box) explain(_ context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, explainOut, error) {
	rule, ok := rules.Lookup(strings.TrimSpace(in.RuleID))
	if !ok {
		return nil, explainOut{}, fmt.Errorf("no rule %q", in.RuleID)
	}
	return nil, explainOut{
		ID:      rule.ID,
		Level:   levelWord(rule.Severity),
		Title:   rule.Title,
		Meaning: plain(rule.Explanation),
		Fix:     plain(rule.Fix),
	}, nil
}

func rejectWrites(live *bool, out, comment *string) error {
	if live != nil {
		return fmt.Errorf("live is not available over mcp")
	}
	if out != nil || comment != nil {
		return fmt.Errorf("file writes are not available over mcp")
	}
	return nil
}

type mode struct {
	api  string
	full bool
}

func checkMode(api, proof string) (mode, error) {
	api = strings.TrimSpace(api)
	if api == "" {
		api = "off"
	}
	switch api {
	case "auto", "off", "require":
	default:
		return mode{}, fmt.Errorf("api_server must be auto, off, or require")
	}
	proof = strings.TrimSpace(proof)
	if proof == "" {
		proof = "default"
	}
	switch proof {
	case "default":
		return mode{api: api}, nil
	case "full":
		return mode{api: api, full: true}, nil
	default:
		return mode{}, fmt.Errorf("values_proof must be default or full")
	}
}

func loadFacts(path string) (*facts.Document, error) {
	if path == "" {
		return nil, nil
	}
	doc, err := facts.Load(path)
	if err != nil {
		return nil, fmt.Errorf("cluster facts: %w", err)
	}
	return doc, nil
}

func reportMap(r *verdict.Report) (map[string]any, error) {
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, r); err != nil {
		return nil, err
	}
	return decodeObject(&buf)
}

func combinedMap(c *report.Combined) (map[string]any, error) {
	var buf bytes.Buffer
	if err := report.WriteCombinedJSON(&buf, c); err != nil {
		return nil, err
	}
	return decodeObject(&buf)
}

func decodeObject(r io.Reader) (map[string]any, error) {
	var m map[string]any
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func relLabel(root, abs string) string {
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(abs)
	}
	return rel
}

func levelWord(sev verdict.Severity) string {
	switch sev {
	case verdict.SeverityBlocker:
		return "ERROR"
	case verdict.SeverityInfo:
		return "INFO"
	default:
		return "WARN"
	}
}

func plain(s string) string {
	return strings.ReplaceAll(s, "%%", "")
}

func safeRef(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	return true
}

func gitDiff(ctx context.Context, root, base, head string) ([]string, error) {
	out, err := gitOutput(ctx, root, "diff", "--name-only", base+"..."+head)
	if err != nil {
		out, err = gitOutput(ctx, root, "diff", "--name-only", base, head)
		if err != nil {
			return nil, fmt.Errorf("git diff %s..%s failed", base, head)
		}
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+4)
	full = append(full, "-C", root, "-c", "safe.directory=*")
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	return cmd.Output()
}

func archiveChart(ctx context.Context, root, ref, rel string) (string, func(), error) {
	if strings.TrimSpace(rel) == "" {
		return "", nil, fmt.Errorf("empty chart path")
	}
	tmp, err := os.MkdirTemp("", "stageproof-mcp-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	archive, err := gitOutput(ctx, root, "archive", "--format=tar", ref, rel)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	tar := exec.CommandContext(ctx, "tar", "-x", "-C", tmp)
	tar.Stdin = bytes.NewReader(archive)
	if err := tar.Run(); err != nil {
		cleanup()
		return "", nil, err
	}
	dir := filepath.Join(tmp, rel)
	if st, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil || st.IsDir() {
		cleanup()
		return "", nil, fmt.Errorf("chart %s is not in %s", rel, ref)
	}
	return dir, cleanup, nil
}
