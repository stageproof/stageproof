package rehearse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBaselineTimeoutDetailKeepsTheDeadline(t *testing.T) {
	diag := "level=warning msg=\"unrecognized format \\\"int64\\\"\"\nError: INSTALLATION FAILED: context deadline exceeded"
	if got := baselineTimeoutDetail(diag); got != "context deadline exceeded" {
		t.Fatalf("got %q", got)
	}
	summary := baselineSlowSummary("600s", diag)
	if !strings.Contains(summary, "baseline slow:") || !strings.Contains(summary, "Detail: context deadline exceeded.") {
		t.Fatalf("summary: %s", summary)
	}
	if strings.Contains(summary, "int64") {
		t.Fatalf("int64 warning leaked into the summary: %s", summary)
	}
	if !strings.Contains(upgradeSummary(0, true), "had not become ready") {
		t.Fatal(upgradeSummary(0, true))
	}
}

func TestLatestPatchTagPicksHighestPatchOfThatMinor(t *testing.T) {
	tags := []string{"v1.33.0", "v1.33.1", "v1.33.12", "v1.33.2", "v1.33.7", "v1.3.9", "v1.34.1", "v1.33.4-rc1", "latest"}
	if got := latestPatchTag(tags, "1.33"); got != "v1.33.12" {
		t.Fatalf("patch 12 is newer than patch 7, got %q", got)
	}
	if got := latestPatchTag(tags, "1.35"); got != "" {
		t.Fatalf("nothing is published for 1.35 in this list, got %q", got)
	}
}

func TestMinorOf(t *testing.T) {
	for in, want := range map[string]string{"1.35": "1.35", "v1.35.8": "1.35", " 1.33.0 ": "1.33", "1": "", "": "", "latest": "", "v1.x": ""} {
		if got := minorOf(in); got != want {
			t.Fatalf("minorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveNodeImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "v1.35." {
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"name":"v1.35.0"},{"name":"v1.35.8"},{"name":"v1.35.5"}]}`))
	}))
	defer srv.Close()
	prev := nodeImageTagsURL
	nodeImageTagsURL = srv.URL
	defer func() { nodeImageTagsURL = prev }()

	if got := resolveNodeImage(context.Background(), "1.35"); got != "kindest/node:v1.35.8" {
		t.Fatalf("resolveNodeImage(1.35) = %q", got)
	}
	if got := resolveNodeImage(context.Background(), "1.99"); got != "" {
		t.Fatalf("an unpublished version must fall back to kind's default, got %q", got)
	}
}

// After a broken upgrade the crashing pod of the bad version is still around
// when the rollback starts. It must not be read as a failed rollback.
func TestLeftoverFailingPodsAreNotBlamedOnTheNextStep(t *testing.T) {
	pods := []byte(`{"items":[
	  {"metadata":{"name":"web-bad-1"},
	   "status":{"containerStatuses":[{"name":"app","restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
	  {"metadata":{"name":"web-bad-2","deletionTimestamp":"2026-11-16T22:00:00Z"},
	   "status":{"containerStatuses":[{"name":"app","restartCount":1,"state":{"terminated":{"exitCode":1,"reason":"Error"}}}]}},
	  {"metadata":{"name":"web-good-1"},
	   "status":{"containerStatuses":[{"name":"app","ready":true,"restartCount":0,"state":{}}]}}
	]}`)

	failing := failingPodsFromJSON(pods)
	if len(failing) != 1 || failing["web-bad-1"] == "" {
		t.Fatalf("only the crashing pod that is not being deleted counts, got %v", failing)
	}

	before := map[string]string{"web-bad-1": "CrashLoopBackOff (restarts=1)"}
	if got := newFailure(failing, before); got != "" {
		t.Fatalf("a pod that was already failing before the step is not a new failure, got %q", got)
	}
	if got := newFailure(failing, nil); got != "web-bad-1: CrashLoopBackOff (restarts=2)" {
		t.Fatalf("a pod that starts failing during the step must be reported, got %q", got)
	}
}

func TestResolveNodeImageFallsBackWhenLookupFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	prev := nodeImageTagsURL
	nodeImageTagsURL = srv.URL
	defer func() { nodeImageTagsURL = prev }()

	if got := resolveNodeImage(context.Background(), "1.35"); got != "" {
		t.Fatalf("a failed lookup must not stop the rehearsal, got %q", got)
	}
}

func TestLastLinesKeepsTheEndOfTheLog(t *testing.T) {
	log := "Waiting for caches to sync\nv1 Endpoints is deprecated in v1.33+\nvolume-manager: connection to the local volume manager failed\n"
	got := lastLines(log, 2, 800)
	if !strings.Contains(got, "volume-manager") || strings.Contains(got, "Waiting for caches") {
		t.Fatalf("got %q", got)
	}
}

func TestHardImageFailureLeavesTransientPullsAlone(t *testing.T) {
	if hardImageFailure("failed to pull image: net/http: request canceled") {
		t.Fatal("a canceled pull can succeed on retry")
	}
	if !hardImageFailure("manifest unknown: not found") {
		t.Fatal("a deleted tag must stay a hard failure")
	}
}

func TestPrivateRegistryComesFromThePullError(t *testing.T) {
	if privateRegistryPull("failed to pull registry.example/app:1: connection reset by peer") {
		t.Fatal("a host name is not a private registry")
	}
	if !privateRegistryPull("Head \"https://registry.example/v2/app/manifests/1\": 403 Forbidden") {
		t.Fatal("403 is a private registry")
	}
	if !privateRegistryPull("dial tcp: lookup registry.example: no such host") {
		t.Fatal("a host that does not resolve is a private registry")
	}
}
