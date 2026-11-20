package apiserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestVersionFromGit(t *testing.T) {
	if got := versionFromGit("v1.35.0+k3s1"); got != "1.35.0" {
		t.Fatalf("git version: %s", got)
	}
	if got := versionFromGit("1.36.0"); got != "1.36.0" {
		t.Fatalf("plain version: %s", got)
	}
}

func TestStartedBinDirUsesRequestedPatch(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "1.35.0-"+runtime.GOOS+"-"+runtime.GOARCH)
	neu := filepath.Join(root, "1.36.0-"+runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(neu, 0o755); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(old, "kube-apiserver")
	if err := os.WriteFile(started, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(neu, "kube-apiserver"), []byte("y"), 0o755); err != nil {
		t.Fatal(err)
	}

	env := &envtest.Environment{BinaryAssetsDirectory: root}
	env.ControlPlane.GetAPIServer().Path = started
	got := startedBinDir(env, "", versionFromGit("v1.35.0"))
	if got != old {
		t.Fatalf("1.35 resolved %s, want %s", got, old)
	}
	if got == neu {
		t.Fatal("a newer cache directory was selected")
	}
}
