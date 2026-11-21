package supply

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestSupplyDefaults(t *testing.T) {
	if DefaultRequestTimeout != 5*time.Second || DefaultBudget != 20*time.Second {
		t.Fatalf("timeout %s budget %s", DefaultRequestTimeout, DefaultBudget)
	}
	if maxPerHost != 4 {
		t.Fatalf("per host %d", maxPerHost)
	}
}

func TestSupplyRulesAgainstALocalRegistry(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	host := hostOf(srv)

	pushImage(t, host, "app:ok", mustImage(t, "linux", "amd64"))
	pushIndex(t, host, "multi:1", []v1.Image{
		mustImage(t, "linux", "amd64"),
		mustImage(t, "linux", "arm64"),
	})
	pushIndex(t, host, "arm:1", []v1.Image{mustImage(t, "linux", "arm64")})
	pushImage(t, host, "new/app:1", mustImage(t, "linux", "amd64"))

	opt := Options{Insecure: true, Timeout: 2 * time.Second, Budget: 10 * time.Second}
	use := func(resource, image string) helm.ImageUse {
		return helm.ImageUse{Key: resource, Resource: resource, Image: image}
	}

	t.Run("not found", func(t *testing.T) {
		got := Check(context.Background(), nil, []helm.ImageUse{use("Deployment/default/demo", host+"/app:missing")}, false, opt)
		mustRule(t, got, "supply.image.not_found", verdict.SeverityBlocker)
	})
	t.Run("existing image is not a finding", func(t *testing.T) {
		got := Check(context.Background(), nil, []helm.ImageUse{use("Deployment/default/demo", host+"/app:ok")}, false, opt)
		if len(got) != 0 {
			t.Fatalf("findings %#v", got)
		}
	})
	t.Run("multi arch image has the platform", func(t *testing.T) {
		got := Check(context.Background(), nil, []helm.ImageUse{use("Deployment/default/demo", host+"/multi:1")}, false, opt)
		if hasRule(got, "supply.image.platform_missing") || hasRule(got, "supply.image.not_found") {
			t.Fatalf("findings %#v", got)
		}
	})
	t.Run("platform missing", func(t *testing.T) {
		got := Check(context.Background(), nil, []helm.ImageUse{use("Deployment/default/demo", host+"/arm:1")}, false, opt)
		mustRule(t, got, "supply.image.platform_missing", verdict.SeverityBlocker)
	})
	t.Run("moved", func(t *testing.T) {
		got := Check(context.Background(),
			[]helm.ImageUse{use("Deployment/default/demo", host+"/old/app:1")},
			[]helm.ImageUse{use("Deployment/default/demo", host+"/new/app:1")},
			false, opt)
		mustRule(t, got, "supply.image.moved", verdict.SeverityInfo)
		if hasRule(got, "supply.image.not_found") {
			t.Fatalf("existing destination was reported missing: %#v", got)
		}
	})
	t.Run("unchanged image is not checked", func(t *testing.T) {
		image := host + "/app:missing"
		got := Check(context.Background(),
			[]helm.ImageUse{use("Deployment/default/demo", image)},
			[]helm.ImageUse{use("Deployment/default/demo", image)},
			false, opt)
		if len(got) != 0 {
			t.Fatalf("unchanged image produced %#v", got)
		}
	})
	t.Run("chart deprecated", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: web\nversion: 1.0.0\ndeprecated: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !ChartDeprecated(dir) {
			t.Fatal("deprecated chart was not read")
		}
		plain := t.TempDir()
		if err := os.WriteFile(filepath.Join(plain, "Chart.yaml"), []byte("apiVersion: v2\nname: web\nversion: 1.0.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ChartDeprecated(plain) {
			t.Fatal("plain chart was deprecated")
		}
		got := Check(context.Background(), nil, nil, true, Options{Offline: true})
		mustRule(t, got, "supply.chart.deprecated", verdict.SeverityWarning)
	})
}

func TestSupplyRefusesToBlockWhenTheRegistryDoesNotProveAbsence(t *testing.T) {
	opt := Options{Insecure: true, Timeout: 300 * time.Millisecond, Budget: 2 * time.Second}
	use := func(image string) []helm.ImageUse {
		return []helm.ImageUse{{Key: "k", Resource: "Deployment/default/demo", Image: image}}
	}
	t.Run("unauthorized stays info", func(t *testing.T) {
		srv := httptest.NewServer(statusHandler(http.StatusUnauthorized, `{"errors":[{"code":"NAME_UNKNOWN","message":"nope"}]}`))
		t.Cleanup(srv.Close)
		got := Check(context.Background(), nil, use(hostOf(srv)+"/private:missing"), false, opt)
		mustRule(t, got, "supply.image.unverifiable", verdict.SeverityInfo)
		if hasRule(got, "supply.image.not_found") {
			t.Fatalf("401 was treated as missing: %#v", got)
		}
		if blocker(got) {
			t.Fatal("unauthorized blocked")
		}
	})
	t.Run("rate limit stays info", func(t *testing.T) {
		srv := httptest.NewServer(statusHandler(http.StatusTooManyRequests, `{"errors":[{"code":"TOOMANYREQUESTS"}]}`))
		t.Cleanup(srv.Close)
		got := Check(context.Background(), nil, use(hostOf(srv)+"/app:1"), false, opt)
		mustRule(t, got, "supply.registry_unreachable", verdict.SeverityInfo)
		if blocker(got) {
			t.Fatal("rate limit blocked")
		}
	})
	t.Run("slow registry stays passed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Second)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		got := Check(context.Background(), nil, use(hostOf(srv)+"/app:1"), false, opt)
		mustRule(t, got, "supply.registry_unreachable", verdict.SeverityInfo)
		if blocker(got) {
			t.Fatal("timeout blocked")
		}
	})
}

func TestOfflineMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	got := Check(context.Background(), nil, []helm.ImageUse{{
		Key: "k", Resource: "Deployment/default/demo", Image: hostOf(srv) + "/app:missing",
	}}, false, Options{Offline: true, Insecure: true})
	if hits.Load() != 0 {
		t.Fatalf("offline made %d requests", hits.Load())
	}
	if len(got) != 0 {
		t.Fatalf("findings %#v", got)
	}
}

func TestFindingsAreSorted(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	host := hostOf(srv)
	opt := Options{Insecure: true, Timeout: time.Second, Budget: 5 * time.Second}
	after := []helm.ImageUse{
		{Key: "b", Resource: "Deployment/default/b", Image: host + "/app:b"},
		{Key: "a", Resource: "Deployment/default/a", Image: host + "/app:a"},
	}
	first := Check(context.Background(), nil, after, false, opt)
	second := Check(context.Background(), nil, after, false, opt)
	if len(first) != 2 || first[0].Resource > first[1].Resource {
		t.Fatalf("order %#v", first)
	}
	if len(second) != len(first) || first[0].Resource != second[0].Resource || first[1].Message != second[1].Message {
		t.Fatalf("not stable\n%#v\n%#v", first, second)
	}
}

func TestClassifyDoesNotTreatUnauthorizedAsMissing(t *testing.T) {
	err := &transport.Error{
		StatusCode: http.StatusUnauthorized,
		Errors:     []transport.Diagnostic{{Code: "NAME_UNKNOWN", Message: "hidden"}},
	}
	if classify(err) != kindUnverifiable {
		t.Fatalf("classify = %v", classify(err))
	}
	missing := &transport.Error{StatusCode: http.StatusNotFound, Errors: []transport.Diagnostic{{Code: "MANIFEST_UNKNOWN"}}}
	if classify(missing) != kindMissing {
		t.Fatalf("404 classify = %v", classify(missing))
	}
}

func hostOf(srv *httptest.Server) string {
	return srv.Listener.Addr().String()
}

func statusHandler(code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

func mustImage(t *testing.T, osName, arch string) v1.Image {
	t.Helper()
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf.OS = osName
	cf.Architecture = arch
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func pushImage(t *testing.T, host, repoTag string, img v1.Image) {
	t.Helper()
	ref, err := name.ParseReference(host+"/"+repoTag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img, remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatal(err)
	}
}

func pushIndex(t *testing.T, host, repoTag string, images []v1.Image) {
	t.Helper()
	adds := make([]mutate.IndexAddendum, 0, len(images))
	for _, img := range images {
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		adds = append(adds, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{
				OS:           cf.OS,
				Architecture: cf.Architecture,
			}},
		})
	}
	idx := mutate.AppendManifests(empty.Index, adds...)
	ref, err := name.ParseReference(host+"/"+repoTag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx, remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatal(err)
	}
}

func mustRule(t *testing.T, got []verdict.Finding, id string, sev verdict.Severity) {
	t.Helper()
	for _, f := range got {
		if f.RuleID == id && f.Severity == sev {
			return
		}
	}
	t.Fatalf("missing %s %s in %#v", id, sev, got)
}

func hasRule(got []verdict.Finding, id string) bool {
	for _, f := range got {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func blocker(got []verdict.Finding) bool {
	for _, f := range got {
		if f.Severity == verdict.SeverityBlocker {
			return true
		}
	}
	return false
}
