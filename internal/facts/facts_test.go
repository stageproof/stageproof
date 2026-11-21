package facts

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestHelmSecretDropsValues(t *testing.T) {
	older := helmSecret(t, "default", "sh.helm.release.v1.web.v1", "web", "1", "1.0.0", "1", "old-secret-value")
	newer := helmSecret(t, "default", "sh.helm.release.v1.web.v2", "web", "2", "1.2.3", "9.9.9", "do-not-export-this")
	plain := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
		Data: map[string][]byte{
			"values":     []byte("do-not-export-this"),
			"data":       []byte("also-secret"),
			"stringData": []byte("also-secret"),
		},
	}
	charts, err := chartsFromHelmSecrets([]corev1.Secret{older, plain, newer})
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != 1 {
		t.Fatalf("charts: %+v", charts)
	}
	if charts[0].Source != sourceHelm || charts[0].Name != "web" || charts[0].Version != "1.2.3" || charts[0].AppVersion != "9.9.9" || charts[0].Release != "web" || charts[0].Namespace != "default" {
		t.Fatalf("chart: %+v", charts[0])
	}
	raw, err := json.Marshal(charts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-export-this") || strings.Contains(string(raw), "old-secret-value") || strings.Contains(string(raw), "also-secret") {
		t.Fatalf("secret material in chart json: %s", raw)
	}
	assertNoSecretKeys(t, raw)
}

func TestApplicationAndHelmReleaseDropValues(t *testing.T) {
	app := map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "argocd"},
		"spec": map[string]any{
			"destination": map[string]any{"namespace": "prod"},
			"source": map[string]any{
				"repoURL":        "https://charts.example.com",
				"chart":          "web",
				"targetRevision": "1.2.3",
				"helm":           map[string]any{"values": "password: do-not-export-this"},
			},
			"sources": []any{
				map[string]any{"repoURL": "https://git.example.com", "path": "charts/web"},
				map[string]any{"repoURL": "https://charts.example.com", "chart": "extra", "targetRevision": "2.0.0", "helm": map[string]any{"valuesObject": map[string]any{"token": "do-not-export-this"}}},
			},
		},
	}
	rel := map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "flux"},
		"spec": map[string]any{
			"releaseName":     "web",
			"targetNamespace": "prod",
			"chart": map[string]any{
				"spec": map[string]any{"chart": "web", "version": "4.5.6"},
			},
			"values": map[string]any{"password": "do-not-export-this"},
		},
	}
	charts := append(chartsFromApplication(app), chartsFromHelmRelease(rel)...)
	raw, err := json.Marshal(charts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-export-this") || bytes.Contains(raw, []byte(`"values"`)) || bytes.Contains(raw, []byte(`"valuesObject"`)) {
		t.Fatalf("values copied: %s", raw)
	}
	if len(charts) != 3 {
		t.Fatalf("charts: %+v", charts)
	}
	assertNoSecretKeys(t, raw)
}

func TestPlatformsAndMinor(t *testing.T) {
	doc := &Document{
		ServerVersion: "v1.35.2+abc",
		Nodes: []Node{
			{Name: "b", Architecture: "arm64", OperatingSystem: "linux"},
			{Name: "a", Architecture: "amd64", OperatingSystem: "linux"},
			{Name: "c", Architecture: "arm64", OperatingSystem: "linux"},
			{Name: "d", Architecture: "amd64"},
		},
	}
	got := doc.Platforms()
	if strings.Join(got, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("platforms: %v", got)
	}
	if doc.KubernetesMinor() != "1.35" {
		t.Fatalf("minor: %s", doc.KubernetesMinor())
	}
	if (&Document{}).Platforms() != nil {
		t.Fatal("empty facts must leave the default platform alone")
	}
}

func TestClusterRoleIsGetAndList(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "facts-clusterrole.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatal(err)
	}
	if role.Name != "stageproof-facts" {
		t.Fatalf("name: %s", role.Name)
	}
	want := map[string]bool{}
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" {
				t.Fatalf("verb %s in %+v", verb, rule)
			}
		}
		for _, resource := range rule.Resources {
			if resource == "*" {
				t.Fatalf("wildcard resource: %+v", rule)
			}
			want[resource] = true
		}
	}
	for _, resource := range []string{
		"nodes", "secrets", "customresourcedefinitions",
		"validatingadmissionpolicies", "validatingadmissionpolicybindings",
		"storageclasses", "applications", "helmreleases",
	} {
		if !want[resource] {
			t.Fatalf("missing %s", resource)
		}
	}
}

func TestLoadRejectsASpecThatIsNotAnObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "facts.json")
	body := `{"schema_version":1,"server_version":"1.35.0","crds":[{"name":"widgets.example.com","spec":"nope"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a spec that is not an object must be rejected")
	}
}

func helmSecret(t *testing.T, namespace, name, release, revision, version, appVersion, password string) corev1.Secret {
	t.Helper()
	payload := map[string]any{
		"name":      release,
		"namespace": namespace,
		"config":    map[string]any{"password": password},
		"chart": map[string]any{
			"metadata": map[string]any{"name": release, "version": version, "appVersion": appVersion},
			"values":   map[string]any{"password": password},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"name": release, "owner": "helm", "version": revision},
		},
		Type: helmSecretType,
		Data: map[string][]byte{"release": []byte(encoded)},
	}
}

func assertNoSecretKeys(t *testing.T, raw []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(n any) {
		switch x := n.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "values" || k == "data" || k == "stringData" {
					t.Fatalf("key %s in %s", k, raw)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
}
