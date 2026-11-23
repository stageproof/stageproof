package comment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "super-secret-token-value"

func TestGitHubCreatesWhenMarkerIsAbsent(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if strings.Contains(r.URL.String(), testToken) {
			t.Errorf("token leaked into URL %s", r.URL.String())
		}
		switch {
		case r.Method == http.MethodGet:
			writeJSON(w, []note{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/7/comments":
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal([]byte(readBody(t, r)), &payload); err != nil {
				t.Errorf("body: %v", err)
			}
			if strings.Count(payload.Body, DefaultMarker) != 1 || !strings.Contains(payload.Body, "hello") {
				t.Errorf("body %q", payload.Body)
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, note{ID: 9, Body: "ok"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	code, out, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": testToken}, "hello\n")
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errb)
	}
	if !strings.Contains(out, "Posted Stageproof comment on pull request #7") {
		t.Fatalf("stdout %q", out)
	}
	if strings.Contains(out+errb, testToken) {
		t.Fatalf("token leaked: %s %s", out, errb)
	}
	if len(calls) != 2 || calls[1] != "POST /repos/acme/widgets/issues/7/comments" {
		t.Fatalf("calls %v", calls)
	}
}

func TestGitHubUpdatesWhenMarkerIsPresent(t *testing.T) {
	var patched string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") == "":
			w.Header().Set("Link", `<`+srvURL(r)+`/repos/acme/widgets/issues/7/comments?page=2>; rel="next"`)
			writeJSON(w, []note{{ID: 1, Body: "unrelated"}})
		case r.Method == http.MethodGet && r.URL.Query().Get("page") == "2":
			writeJSON(w, []note{{ID: 42, Body: DefaultMarker + "\nold"}})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/widgets/issues/comments/42":
			patched = readBody(t, r)
			writeJSON(w, note{ID: 42})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	code, out, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": testToken}, "new text\n")
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errb)
	}
	if !strings.Contains(out, "Updated Stageproof comment on pull request #7") {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(patched, "new text") || strings.Contains(patched, "POST") {
		t.Fatalf("patched %s", patched)
	}
}

func TestGitLabCreatesAndUpdates(t *testing.T) {
	var sawJob, sawPrivate bool
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("JOB-TOKEN") != "" {
			sawJob = true
		}
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			sawPrivate = true
		}
		if strings.Contains(r.URL.String(), testToken) {
			t.Errorf("token in URL")
		}
		switch {
		case r.Method == http.MethodGet && !strings.Contains(r.URL.RawQuery, "page=2"):
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, []note{{ID: 3, Body: "other"}})
		case r.Method == http.MethodGet:
			writeJSON(w, []note{{ID: 8, Body: "prefix " + DefaultMarker}})
		default:
			method = r.Method + " " + r.URL.EscapedPath()
			writeJSON(w, note{ID: 8})
		}
	}))
	defer srv.Close()

	code, out, errb := runComment(t, srv, Options{
		Provider: "gitlab",
		Repo:     "group/project",
		MR:       "4",
	}, envMap{"CI_JOB_TOKEN": testToken}, "note\n")
	if code != 0 || !strings.Contains(out, "Updated Stageproof comment on merge request !4") {
		t.Fatalf("exit %d out %q err %s", code, out, errb)
	}
	if method != "PUT /projects/group%2Fproject/merge_requests/4/notes/8" {
		t.Fatalf("write %s", method)
	}
	if !sawJob || sawPrivate {
		t.Fatalf("job token header job=%v private=%v", sawJob, sawPrivate)
	}

	created := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("JOB-TOKEN") != "" || r.Header.Get("PRIVATE-TOKEN") != testToken {
			t.Errorf("headers job=%q private=%q", r.Header.Get("JOB-TOKEN"), r.Header.Get("PRIVATE-TOKEN"))
		}
		if r.Method == http.MethodGet {
			writeJSON(w, []note{})
			return
		}
		method = r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, note{ID: 1})
	}))
	defer created.Close()
	code, out, errb = runComment(t, created, Options{
		Provider: "gitlab",
		Repo:     "15",
		MR:       "4",
	}, envMap{"GITLAB_TOKEN": testToken, "CI_JOB_TOKEN": "other-job-token-value"}, "fresh\n")
	if code != 0 || !strings.Contains(out, "Posted Stageproof comment on merge request !4") {
		t.Fatalf("create exit %d out %q err %s", code, out, errb)
	}
	if method != "POST /projects/15/merge_requests/4/notes" {
		t.Fatalf("create %s", method)
	}
}

func TestScrubsTokenFromHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, []note{})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"bad `+testToken+`"}`)
	}))
	defer srv.Close()

	code, out, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": testToken}, "hello\n")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(out+errb, testToken) || !strings.Contains(errb, "[redacted]") {
		t.Fatalf("stderr %q", errb)
	}
}

func TestForbiddenNamesThePermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, testToken)
	}))
	defer srv.Close()

	opt := Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}
	env := envMap{"GITHUB_TOKEN": testToken}
	code, _, errb := runComment(t, srv, opt, env, "hello\n")
	if code != 2 || !strings.Contains(errb, "write pull request comments") || strings.Contains(errb, testToken) {
		t.Fatalf("exit %d stderr %q", code, errb)
	}
	opt.BestEffort = true
	code, _, errb = runComment(t, srv, opt, env, "hello\n")
	if code != 0 || strings.Contains(errb, testToken) || !strings.Contains(errb, "HTTP 403") {
		t.Fatalf("best-effort exit %d stderr %q", code, errb)
	}
}

func TestMissingFileAndEnvExit2(t *testing.T) {
	var out, errb strings.Builder
	code := Run(context.Background(), Options{
		File:   filepath.Join(t.TempDir(), "missing.md"),
		Stderr: &errb,
		Stdout: &out,
		Env:    envMap{}.get,
	})
	if code != 2 || !strings.Contains(errb.String(), "open ") {
		t.Fatalf("missing file exit %d err %q", code, errb.String())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "comment.md")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	code = Run(context.Background(), Options{
		File:     path,
		Provider: "auto",
		Stderr:   &errb,
		Stdout:   &out,
		Env:      envMap{}.get,
	})
	if code != 2 || !strings.Contains(errb.String(), "GITHUB_ACTIONS") {
		t.Fatalf("missing env exit %d err %q", code, errb.String())
	}

	errb.Reset()
	code = Run(context.Background(), Options{
		Provider: "github",
		Stderr:   &errb,
		Stdout:   &out,
		Env:      envMap{"GITHUB_TOKEN": testToken}.get,
	})
	if code != 2 || !strings.Contains(errb.String(), "--file is required") {
		t.Fatalf("missing flag exit %d err %q", code, errb.String())
	}
}

func TestRejectsUnsafeTokenAndDoesNotCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	code, _, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": "bad\ntoken-value"}, "hello\n")
	if code != 2 || called || strings.Contains(errb, "bad\ntoken") {
		t.Fatalf("exit %d called %v stderr %q", code, called, errb)
	}
}

func TestRefusesRedirectToAnotherHost(t *testing.T) {
	otherHits := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("token forwarded")
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	code, _, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": testToken}, "hello\n")
	if code != 2 || otherHits != 0 || strings.Contains(errb, testToken) {
		t.Fatalf("exit %d hits %d stderr %q", code, otherHits, errb)
	}
}

func TestAutoDetectsPullRequestNumber(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		if r.Method == http.MethodGet {
			writeJSON(w, []note{})
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, note{ID: 1})
	}))
	defer srv.Close()

	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":15},"number":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := runComment(t, srv, Options{Provider: "auto"}, envMap{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_REPOSITORY": "acme/widgets",
		"GITHUB_EVENT_PATH": event,
		"GITHUB_TOKEN":      testToken,
	}, "hello\n")
	if code != 0 || !strings.Contains(got, "/issues/15/comments") {
		t.Fatalf("exit %d path %s err %s", code, got, errb)
	}

	code, _, errb = runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
	}, envMap{
		"GITHUB_REF":   "refs/pull/9/merge",
		"GITHUB_TOKEN": testToken,
	}, "hello\n")
	if code != 0 || !strings.Contains(got, "/issues/9/comments") {
		t.Fatalf("ref path %s exit %d err %s", got, code, errb)
	}
}

func TestUsesConfiguredAPIHost(t *testing.T) {
	gh, err := resolve(Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
		Env:      envMap{"GITHUB_TOKEN": testToken, "GITHUB_API_URL": "https://github.example/api/v3"}.get,
	})
	if err != nil || gh.api.String() != "https://github.example/api/v3" {
		t.Fatalf("github api %v err %v", gh.api, err)
	}
	gl, err := resolve(Options{
		Provider: "gitlab",
		Repo:     "15",
		MR:       "4",
		Env:      envMap{"GITLAB_TOKEN": testToken, "CI_API_V4_URL": "https://gitlab.example/api/v4"}.get,
	})
	if err != nil || gl.api.String() != "https://gitlab.example/api/v4" {
		t.Fatalf("gitlab api %v err %v", gl.api, err)
	}
}

func TestRejectsFileAPIURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comment.md")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errb strings.Builder
	code := Run(context.Background(), Options{
		File:     path,
		Provider: "gitlab",
		Repo:     "15",
		MR:       "4",
		APIBase:  "file:///tmp/notes",
		Env:      envMap{"GITLAB_TOKEN": testToken}.get,
		Stderr:   &errb,
		Stdout:   io.Discard,
	})
	if code != 2 || !strings.Contains(errb.String(), "https") {
		t.Fatalf("exit %d err %q", code, errb.String())
	}
}

func TestDoesNotDoubleTheMarker(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, []note{})
			return
		}
		raw = readBody(t, r)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, note{ID: 1})
	}))
	defer srv.Close()
	code, _, errb := runComment(t, srv, Options{
		Provider: "github",
		Repo:     "acme/widgets",
		PR:       "7",
	}, envMap{"GITHUB_TOKEN": testToken}, DefaultMarker+"\nhello\n")
	if code != 0 {
		t.Fatal(errb)
	}
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Count(payload.Body, DefaultMarker) != 1 {
		t.Fatalf("body %q", payload.Body)
	}
}

type envMap map[string]string

func (e envMap) get(k string) string { return e[k] }

func runComment(t *testing.T, srv *httptest.Server, opt Options, env envMap, fileText string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "comment.md")
	if err := os.WriteFile(path, []byte(fileText), 0o644); err != nil {
		t.Fatal(err)
	}
	opt.File = path
	opt.APIBase = srv.URL
	opt.AllowHTTP = true
	opt.Env = env.get
	var out, errb strings.Builder
	opt.Stdout = &out
	opt.Stderr = &errb
	code := Run(context.Background(), opt)
	return code, out.String(), errb.String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func srvURL(r *http.Request) string {
	return "http://" + r.Host
}
