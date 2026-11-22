package rehearse

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/config"
)

func TestSmokeScrubsUserValues(t *testing.T) {
	out := &Outcome{}
	ok := runSmoke(context.Background(), out, "", "ns", "rel", "30s", KindOptions{
		Smoke:   []config.SmokeTest{{Name: "leak", Run: `echo supersecretvalue; exit 1`}},
		Secrets: []string{"supersecretvalue"},
	})
	if ok || len(out.Findings) != 1 || out.Findings[0].RuleID != "live.smoke_failed" {
		t.Fatalf("smoke failure = %+v ok=%v", out.Findings, ok)
	}
	if strings.Contains(out.Findings[0].Detail, "supersecretvalue") || !strings.Contains(out.Findings[0].Detail, "[redacted]") {
		t.Fatalf("detail leaked a user value: %s", out.Findings[0].Detail)
	}
	if len(out.Checks) != 1 || out.Checks[0].Name != "smoke: leak" || out.Checks[0].Status != "fail" {
		t.Fatalf("check row = %+v", out.Checks)
	}
}

func TestSmokeStopsAfterTheFirstFailure(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "second")
	out := &Outcome{}
	ok := runSmoke(context.Background(), out, "", "ns", "rel", "30s", KindOptions{
		Smoke: []config.SmokeTest{
			{Name: "first", Run: "exit 1"},
			{Name: "second", Run: "touch " + marker},
		},
	})
	if ok {
		t.Fatal("the first failure must stop the list")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the second command ran")
	}
}

func TestSmokeTimeout(t *testing.T) {
	out := &Outcome{}
	start := time.Now()
	ok := runSmoke(context.Background(), out, "", "ns", "rel", "30s", KindOptions{
		Smoke: []config.SmokeTest{{Name: "slow", Run: "sleep 5", Timeout: "200ms"}},
	})
	if ok || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout did not stop the command, ok=%v after %s", ok, time.Since(start))
	}
	if len(out.Findings) != 1 || !strings.Contains(out.Findings[0].Message, "timed out") {
		t.Fatalf("finding = %+v", out.Findings)
	}
}

func TestSmokeUsesTheTemporaryKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/the-user-cluster")
	text, err := runShell(context.Background(), "/tmp/kind-kubeconfig", []string{"STAGEPROOF_NAMESPACE=demo"}, `printf '%s %s' "$KUBECONFIG" "$STAGEPROOF_NAMESPACE"`)
	if err != nil {
		t.Fatal(err)
	}
	if text != "/tmp/kind-kubeconfig demo" {
		t.Fatalf("env = %q", text)
	}
}
