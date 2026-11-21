package k8sdiff

import (
	"testing"

	"github.com/stageproof/stageproof/internal/k8sschema"
)

func TestNamedListReorderIsNotAChange(t *testing.T) {
	a := mustParse(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
        - name: app
          image: nginx:1
        - name: side
          image: nginx:2
`)
	b := mustParse(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
        - name: side
          image: nginx:2
        - name: app
          image: nginx:1
`)
	if !Same(a, b, nil) {
		t.Fatalf("reordered containers differed: %+v", Diff(a, b, nil))
	}
}

func TestMaskHidesAVolatileField(t *testing.T) {
	first := mustParse(t, `
apiVersion: v1
kind: Secret
metadata:
  name: tok
data:
  token: aaaa
`)
	second := mustParse(t, `
apiVersion: v1
kind: Secret
metadata:
  name: tok
data:
  token: bbbb
`)
	mask := MaskOf(first, second)
	changed := mustParse(t, `
apiVersion: v1
kind: Secret
metadata:
  name: tok
data:
  token: cccc
  other: kept
`)
	effects := Diff(first, changed, mask)
	if len(effects) != 1 || effects[0].Path != "data.other" {
		t.Fatalf("mask did not isolate the stable field: %+v", effects)
	}
}

func mustParse(t *testing.T, raw string) []k8sschema.ManifestObject {
	t.Helper()
	objs, err := k8sschema.ParseManifestStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}
