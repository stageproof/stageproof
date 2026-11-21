package apiserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/stageproof/stageproof/internal/facts"
)

const factsUser = "stageproof-facts"

// TestAPIServerFactsExport runs the exporter under the shipped ClusterRole
// against this process's API server. The audit window starts after the test
// has created the fixtures, so those creates are not part of the assertion.
// Every request in that window must be get or list, and the facts file must
// not contain a values, data, or stringData key.
func TestAPIServerFactsExport(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	srv, err := start(ctx, version, "", startOptions{AuditLogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	admin, err := kubernetes.NewForConfig(srv.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ext, err := extclient.NewForConfig(srv.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := installFactsFixtures(ctx, admin, ext); err != nil {
		t.Fatal(err)
	}
	auth, err := srv.env.AddUser(envtest.User{Name: factsUser}, &rest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	userCfg := auth.Config()
	if len(userCfg.CertData) == 0 || bytes.Equal(userCfg.CertData, srv.cfg.CertData) {
		t.Fatal("exporter is using the admin certificate")
	}

	offset := auditSize(t, logPath)
	doc, err := facts.Export(ctx, userCfg, facts.ExportOptions{HelmReleases: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if dest := os.Getenv("STAGEPROOF_FACTS_SAMPLE"); dest != "" {
		if bin := os.Getenv("STAGEPROOF_FACTS_BIN"); bin != "" {
			kc := filepath.Join(dir, "kubeconfig")
			wrote := &clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{
					"facts": {Server: userCfg.Host, CertificateAuthorityData: userCfg.CAData},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"facts": {ClientCertificateData: userCfg.CertData, ClientKeyData: userCfg.KeyData},
				},
				Contexts: map[string]*clientcmdapi.Context{
					"facts": {Cluster: "facts", AuthInfo: "facts"},
				},
				CurrentContext: "facts",
			}
			if err := clientcmd.WriteToFile(*wrote, kc); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "facts", "export", "--kubeconfig", kc, "--context", "facts", "--out", dest, "--helm-releases")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("facts export: %v\n%s", err, out)
			}
			if err := os.WriteFile(dest+".stdout", out, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertFacts(t, doc, raw)
	assertAudit(t, logPath, offset)

	extraSpec := []byte(`{"group":"example.com","scope":"Namespaced","names":{"kind":"Extra","plural":"extras"},"versions":[{"name":"v1","served":true,"storage":true,"schema":{"openAPIV3Schema":{"type":"object","properties":{"size":{"type":"integer"}}}}}]}`)
	objs, err := (&facts.Document{CRDs: []facts.CRD{{Name: "extras.example.com", Spec: extraSpec}}}).CRDObjects()
	if err != nil {
		t.Fatal(err)
	}
	var preload []Manifest
	for _, obj := range objs {
		preload = append(preload, Manifest{Raw: obj})
	}
	if _, err := srv.Prove(ctx, nil, nil, nil, nil, ProofOptions{Preload: preload}); err != nil {
		t.Fatal(err)
	}
	if _, err := ext.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "extras.example.com", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func installFactsFixtures(ctx context.Context, admin kubernetes.Interface, ext extclient.Interface) error {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return os.ErrInvalid
	}
	roleRaw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "facts-clusterrole.yaml"))
	if err != nil {
		return err
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(roleRaw, &role); err != nil {
		return err
	}
	if _, err := admin.RbacV1().ClusterRoles().Create(ctx, &role, metav1.CreateOptions{}); err != nil {
		return err
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: factsUser},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "User",
			Name:     factsUser,
		}},
	}
	if _, err := admin.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		return err
	}
	for _, item := range []*corev1.Node{node("worker-a", "arm64"), node("worker-b", "amd64")} {
		created, err := admin.CoreV1().Nodes().Create(ctx, item, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		created.Status = item.Status
		if _, err := admin.CoreV1().Nodes().UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Singular: "widget", Kind: "Widget"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1", Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"size": {Type: "integer"}},
					},
				},
			}},
		},
	}
	if _, err := ext.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
		return err
	}
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "fast"},
		Provisioner: "example.com/fast",
	}
	if _, err := admin.StorageV1().StorageClasses().Create(ctx, sc, metav1.CreateOptions{}); err != nil {
		return err
	}
	for _, secret := range []*corev1.Secret{
		releaseSecret("sh.helm.release.v1.web.v1", "1", "1.0.0", "1", "old-secret-value"),
		releaseSecret("sh.helm.release.v1.web.v2", "2", "1.2.3", "9.9.9", "do-not-export-this"),
		{
			ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
			Data: map[string][]byte{
				"values":     []byte("do-not-export-this"),
				"data":       []byte("also-secret"),
				"stringData": []byte("also-secret"),
			},
		},
	} {
		if _, err := admin.CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func node(name, arch string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{
			Architecture:    arch,
			OperatingSystem: "linux",
		}},
	}
}

func releaseSecret(name, revision, version, appVersion, password string) *corev1.Secret {
	payload := map[string]any{
		"name":      "web",
		"namespace": "default",
		"config":    map[string]any{"password": password},
		"chart": map[string]any{
			"metadata": map[string]any{"name": "web", "version": version, "appVersion": appVersion},
			"values":   map[string]any{"password": password},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"name": "web", "owner": "helm", "version": revision},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{"release": []byte(encoded)},
	}
}

func assertFacts(t *testing.T, doc *facts.Document, raw []byte) {
	t.Helper()
	if doc.ServerVersion == "" {
		t.Fatal("server version is empty")
	}
	if strings.Join(doc.Platforms(), ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("platforms: %v nodes=%+v", doc.Platforms(), doc.Nodes)
	}
	var crd *facts.CRD
	for i := range doc.CRDs {
		if doc.CRDs[i].Name == "widgets.example.com" {
			crd = &doc.CRDs[i]
			break
		}
	}
	if crd == nil || crd.StorageVersion != "v1" || !contains(crd.StoredVersions, "v1") {
		t.Fatalf("crd: %+v", doc.CRDs)
	}
	if !contains(doc.StorageClasses, "fast") {
		t.Fatalf("storage classes: %v", doc.StorageClasses)
	}
	if len(doc.Charts) != 1 || doc.Charts[0].Name != "web" || doc.Charts[0].Version != "1.2.3" || doc.Charts[0].AppVersion != "9.9.9" || doc.Charts[0].Source != "helm-release" {
		t.Fatalf("charts: %+v", doc.Charts)
	}
	text := string(raw)
	for _, secret := range []string{"do-not-export-this", "old-secret-value", "also-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("facts contain %s", secret)
		}
	}
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
					t.Fatalf("key %s", k)
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

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func auditSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

type auditEvent struct {
	Verb string `json:"verb"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	ObjectRef struct {
		Resource string `json:"resource"`
	} `json:"objectRef"`
}

func assertAudit(t *testing.T, path string, offset int64) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) < offset {
		t.Fatalf("audit log shrank: %d < %d", len(raw), offset)
	}
	body := raw[offset:]
	if len(body) > 0 && body[0] != '{' {
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
		}
	}
	gets, lists := 0, 0
	var bad []string
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev auditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("audit line: %v\n%s", err, line)
		}
		switch ev.Verb {
		case "get", "list":
			if ev.User.Username == factsUser {
				if ev.Verb == "get" {
					gets++
				} else {
					lists++
				}
			}
		default:
			bad = append(bad, ev.User.Username+" "+ev.Verb+" "+ev.ObjectRef.Resource)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("audit verbs other than get and list: %s", strings.Join(bad, ", "))
	}
	if gets == 0 || lists == 0 {
		t.Fatalf("exporter audit events: %d get, %d list\n%s", gets, lists, body)
	}
}
