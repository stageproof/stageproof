package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCheckCheckPRAndExplain(t *testing.T) {
	root := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "testdata", "clean", "base"), filepath.Join(root, "base"))
	copyTree(t, filepath.Join("..", "..", "testdata", "clean", "head"), filepath.Join(root, "head"))
	cs := dial(t, root)
	stdout := captureStdout(t)

	check := call(t, cs, "check", map[string]any{
		"current_chart": "base",
		"new_chart":     "head",
		"api_server":    "off",
	})
	if check.IsError {
		t.Fatalf("check: %s", toolText(check))
	}
	report := object(t, check)
	if report["schema_version"] != float64(1) || report["verdict"] == "" {
		t.Fatalf("check report %#v", report)
	}

	repo := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "testdata", "clean", "base"), filepath.Join(repo, "chart"))
	git(t, repo, "init", "-b", "main")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "base")
	base := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	if err := os.RemoveAll(filepath.Join(repo, "chart")); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("..", "..", "testdata", "clean", "head"), filepath.Join(repo, "chart"))
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "head")
	head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	prSession := dial(t, repo)
	pr := call(t, prSession, "check_pr", map[string]any{
		"current_git": base,
		"new_git":     head,
		"api_server":  "off",
	})
	if pr.IsError {
		t.Fatalf("check_pr: %s", toolText(pr))
	}
	combined := object(t, pr)
	if combined["schema_version"] != float64(1) || combined["verdict"] == "" || combined["head_git"] == "" {
		t.Fatalf("check_pr report %#v", combined)
	}
	reports, _ := combined["reports"].([]any)
	if len(reports) == 0 {
		t.Fatal("check_pr returned no reports")
	}

	explained := call(t, cs, "explain", map[string]any{"rule_id": "helm.values.silently_dropped"})
	if explained.IsError {
		t.Fatalf("explain: %s", toolText(explained))
	}
	rule := object(t, explained)
	if rule["id"] != "helm.values.silently_dropped" || rule["level"] == "" || rule["title"] == "" || rule["meaning"] == "" || rule["fix"] == "" {
		t.Fatalf("explain %#v", rule)
	}
	if strings.Contains(toolText(explained), "%%") {
		t.Fatalf("markup left in %s", toolText(explained))
	}
	unknown := call(t, cs, "explain", map[string]any{"rule_id": "not.a.rule"})
	if !unknown.IsError || !strings.Contains(toolText(unknown), "not.a.rule") {
		t.Fatalf("unknown rule %#v %s", unknown, toolText(unknown))
	}
	if stdout() != "" {
		t.Fatalf("stdout %q", stdout())
	}
}

func TestRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "testdata", "clean", "head"), filepath.Join(root, "head"))
	outside := t.TempDir()
	secret := "outside-secret-token"
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "after.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := dial(t, root)
	fields := []string{"values", "cluster_facts", "current_chart", "before"}
	cases := []struct {
		name string
		path string
	}{
		{name: "parent", path: "../etc/passwd"},
		{name: "absolute", path: "/etc/passwd"},
		{name: "symlink", path: "leak.txt"},
	}
	for _, field := range fields {
		for _, tc := range cases {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				args := map[string]any{"api_server": "off", "new_chart": "head"}
				if field == "values" {
					args["values"] = []string{tc.path}
				} else {
					args[field] = tc.path
				}
				if field == "before" {
					delete(args, "new_chart")
					args["after"] = "after.yaml"
				}
				res := call(t, cs, "check", args)
				got := toolText(res)
				if !res.IsError || !strings.Contains(got, "outside") {
					t.Fatalf("result error=%v text=%s", res.IsError, got)
				}
				if strings.Contains(got, secret) || strings.Contains(got, "root:x:") {
					t.Fatalf("outside contents leaked: %s", got)
				}
			})
		}
	}
	live := call(t, cs, "check", map[string]any{"new_chart": "head", "live": false, "api_server": "off"})
	if !live.IsError || !strings.Contains(toolText(live), "not available") {
		t.Fatalf("live %#v %s", live.IsError, toolText(live))
	}
	for _, field := range []string{"out", "comment_file"} {
		res := call(t, cs, "check", map[string]any{"new_chart": "head", field: "", "api_server": "off"})
		if !res.IsError || !strings.Contains(toolText(res), "not available") {
			t.Fatalf("%s %#v %s", field, res.IsError, toolText(res))
		}
	}
}

func dial(t *testing.T, root string) *mcp.ClientSession {
	t.Helper()
	srv, err := New(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Close()
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func object(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured %#v text %s", res.StructuredContent, toolText(res))
	}
	return m
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })
	return func() string {
		os.Stdout = old
		_ = w.Close()
		buf, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
