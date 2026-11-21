package k8sdiff

import (
	"strings"
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

func TestEnvReorderInsideAContainerIsNotAChange(t *testing.T) {
	a := mustParse(t, deployWithEnv("A", "1", "B", "2"))
	b := mustParse(t, deployWithEnv("B", "2", "A", "1"))
	if !Same(a, b, nil) {
		t.Fatalf("reordered env differed: %+v", Diff(a, b, nil))
	}
}

func TestEnvValueChangeIsNotTheWholeContainer(t *testing.T) {
	a := mustParse(t, deployWithEnv("A", "1", "B", "2"))
	b := mustParse(t, deployWithEnv("A", "1", "B", "9"))
	effects := Diff(a, b, nil)
	if len(effects) != 1 || !strings.Contains(effects[0].Path, "containers[name=app].env[name=B].value") {
		t.Fatalf("effects: %+v", effects)
	}
}

func TestVolatileEnvMaskKeepsTheImage(t *testing.T) {
	first := mustParse(t, deployImageEnv("nginx:1", "aaa"))
	second := mustParse(t, deployImageEnv("nginx:1", "bbb"))
	mask := MaskOf(first, second)
	changed := mustParse(t, deployImageEnv("nginx:2", "ccc"))
	effects := Diff(first, changed, mask)
	var paths []string
	for _, e := range effects {
		paths = append(paths, e.Path)
		if strings.Contains(e.Path, "env[name=TOKEN]") {
			t.Fatalf("volatile env was not masked: %+v", effects)
		}
	}
	if !strings.Contains(strings.Join(paths, " "), "image") {
		t.Fatalf("stable image was hidden: %+v", effects)
	}
}

func deployWithEnv(n1, v1, n2, v2 string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
        - name: app
          image: nginx:1
          env:
            - name: ` + n1 + `
              value: "` + v1 + `"
            - name: ` + n2 + `
              value: "` + v2 + `"
`
}

func deployImageEnv(image, token string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  template:
    spec:
      containers:
        - name: app
          image: ` + image + `
          env:
            - name: TOKEN
              value: "` + token + `"
            - name: MODE
              value: "on"
`
}

func mustParse(t *testing.T, raw string) []k8sschema.ManifestObject {
	t.Helper()
	objs, err := k8sschema.ParseManifestStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}
