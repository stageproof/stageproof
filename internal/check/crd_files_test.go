package check

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCRDFilesWalksNested(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "crds", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "widgets.yaml"), []byte("kind: CustomResourceDefinition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "charts", "extra", "crds")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "gadgets.yaml"), []byte("kind: CustomResourceDefinition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := crdFiles(dir)
	if len(got) != 2 {
		t.Fatalf("files: %v", got)
	}
}
