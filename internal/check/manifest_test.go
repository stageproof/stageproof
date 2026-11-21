package check

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestLoadRenderedSortsFiles(t *testing.T) {
	dir := t.TempDir()
	body := func(name string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(body("b")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yml"), []byte(body("a")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	objs, err := loadRendered(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Metadata.Name != "a" || objs[1].Metadata.Name != "b" {
		t.Fatalf("files were not read in name order: %+v", objs)
	}
}

func TestManifestModeRunsImpactWithoutAChart(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "manifests")
	rep := Run(context.Background(), Input{
		Adapter:        "manifests",
		ManifestMode:   true,
		ManifestBefore: filepath.Join(root, "before"),
		ManifestAfter:  filepath.Join(root, "after"),
		BaseRef:        "before",
		HeadRef:        "after",
		KubeVersion:    "1.35",
		APIServer:      "off",
		Started:        time.Now(),
	})
	if rep.Verdict != verdict.Blocked || rep.ExitCode != 1 {
		t.Fatalf("dropping a claim must block, got %s exit %d", rep.Verdict, rep.ExitCode)
	}
	if rep.Adapter != "manifests" || rep.Checked != verdict.CheckedManifests {
		t.Fatalf("adapter %q checked %q", rep.Adapter, rep.Checked)
	}
	if !rep.HasRule("impact.delete.pvc") {
		t.Fatalf("missing impact.delete.pvc: %+v", rep.Findings)
	}
	wantSkip := map[string]bool{
		"required values gate": false,
		"helm template":        false,
		"values.yaml AST":      false,
		"upgrade signals":      false,
	}
	sawImpact := false
	sawAPI := false
	for _, c := range rep.Checks {
		if _, ok := wantSkip[c.Name]; ok {
			if c.Status != "skip" {
				t.Fatalf("%s status = %s, want skip", c.Name, c.Status)
			}
			wantSkip[c.Name] = true
		}
		if c.Name == "impact" {
			sawImpact = true
		}
		if c.Name == "k8s 1.35 compatibility" {
			sawAPI = true
		}
	}
	for name, seen := range wantSkip {
		if !seen {
			t.Fatalf("missing skip check %s", name)
		}
	}
	if !sawImpact || !sawAPI {
		t.Fatalf("impact %v api %v checks %+v", sawImpact, sawAPI, rep.Checks)
	}
}

func TestManifestModePassesWhenNothingChanged(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "manifests", "after")
	rep := Run(context.Background(), Input{
		ManifestMode:   true,
		ManifestBefore: root,
		ManifestAfter:  root,
		KubeVersion:    "1.35",
		APIServer:      "off",
		Started:        time.Now(),
	})
	if rep.Verdict != verdict.Passed || rep.ExitCode != 0 {
		t.Fatalf("identical manifests = %s exit %d findings %+v", rep.Verdict, rep.ExitCode, rep.Findings)
	}
	if rep.Adapter != "manifests" {
		t.Fatalf("empty adapter must still be manifests when the mode is set, got %q", rep.Adapter)
	}
}

func TestManifestModeRejectsAMissingPath(t *testing.T) {
	rep := Run(context.Background(), Input{
		ManifestMode:   true,
		ManifestBefore: filepath.Join(t.TempDir(), "missing"),
		ManifestAfter:  filepath.Join(t.TempDir(), "also-missing"),
		KubeVersion:    "1.35",
		APIServer:      "off",
		Started:        time.Now(),
	})
	if rep.Verdict != verdict.Blocked || !rep.HasRule("manifest.invalid_yaml") {
		t.Fatalf("unreadable manifests = %s %+v", rep.Verdict, rep.Findings)
	}
}
