// Package comment posts one Stageproof note on a GitHub pull request or a
// GitLab merge request. A later run updates the note that already contains
// the marker instead of adding another one.
package comment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultMarker is the HTML comment that identifies the note this command owns.
const DefaultMarker = "<!-- stageproof-comment -->"

const (
	maxCommentBytes = 1 << 20
	githubBodyLimit = 65536
	maxPages        = 20
	minTokenLen     = 8
)

// pullRef matches the GitHub Actions ref for a pull request.
var pullRef = regexp.MustCompile(`^refs/pull/([1-9][0-9]*)/(?:merge|head)$`)

// Options is one comment post. Env, when set, replaces the process environment.
// APIBase replaces the detected API host. Tests set AllowHTTP for httptest.
type Options struct {
	File       string
	Provider   string
	PR         string
	MR         string
	Repo       string
	Marker     string
	BestEffort bool
	Env        func(string) string
	HTTP       *http.Client
	APIBase    string
	AllowHTTP  bool
	Stdout     io.Writer
	Stderr     io.Writer
}

type target struct {
	provider string
	api      *url.URL
	repo     string
	number   string
	token    string
	jobToken bool
}

// Run posts or updates the comment. Exit 0 means the note was written, or
// --best-effort skipped a 403. Exit 2 means the comment was not written.
func Run(ctx context.Context, opt Options) int {
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if opt.Env == nil {
		opt.Env = os.Getenv
	}
	if strings.TrimSpace(opt.Marker) == "" {
		opt.Marker = DefaultMarker
	}
	if ctx == nil {
		ctx = context.Background()
	}

	body, code, err := loadBody(opt)
	if err != nil {
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: %s\n", err.Error())
		return code
	}
	tgt, err := resolve(opt)
	if err != nil {
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: %s\n", scrub(err.Error(), ""))
		return 2
	}
	if tgt.provider == "github" && len(body) > githubBodyLimit {
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: GitHub comments cannot be longer than %d characters\n", githubBodyLimit)
		return 2
	}

	client := opt.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	client.CheckRedirect = refuseOffHost(opt.AllowHTTP)

	id, status, respBody, err := findMarker(ctx, client, tgt, opt.Marker)
	if err != nil {
		return finish(opt, tgt, status, respBody, err)
	}
	method, rawURL, action := writePlan(tgt, id)
	status, respBody, err = doJSON(ctx, client, method, rawURL, tgt, body)
	if err != nil {
		return finish(opt, tgt, status, respBody, err)
	}
	_, _ = fmt.Fprintf(opt.Stdout, "%s Stageproof comment on %s\n", action, place(tgt))
	return 0
}

func loadBody(opt Options) (string, int, error) {
	path := strings.TrimSpace(opt.File)
	if path == "" {
		return "", 2, fmt.Errorf("--file is required")
	}
	marker := opt.Marker
	if strings.ContainsAny(marker, "\r\n") || len(marker) > 200 {
		return "", 2, fmt.Errorf("--marker must be one line of at most 200 characters")
	}
	switch strings.ToLower(strings.TrimSpace(opt.Provider)) {
	case "", "auto", "github", "gitlab":
	default:
		return "", 2, fmt.Errorf("--provider must be auto, github, or gitlab")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 2, fmt.Errorf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxCommentBytes+1))
	if err != nil {
		return "", 2, fmt.Errorf("read %s: %v", path, err)
	}
	if len(raw) > maxCommentBytes {
		return "", 2, fmt.Errorf("%s is larger than 1 MiB", path)
	}
	text := string(raw)
	if strings.TrimSpace(text) == "" {
		return "", 2, fmt.Errorf("--file is empty")
	}
	if strings.Contains(text, marker) {
		return text, 0, nil
	}
	return marker + "\n" + text, 0, nil
}

func resolve(opt Options) (target, error) {
	provider := strings.ToLower(strings.TrimSpace(opt.Provider))
	if provider == "" {
		provider = "auto"
	}
	if provider == "auto" {
		switch {
		case truthy(opt.Env("GITHUB_ACTIONS")):
			provider = "github"
		case truthy(opt.Env("GITLAB_CI")):
			provider = "gitlab"
		default:
			return target{}, fmt.Errorf("provider auto found neither GITHUB_ACTIONS nor GITLAB_CI. Pass --provider github or --provider gitlab")
		}
	}
	switch provider {
	case "github":
		return resolveGitHub(opt)
	case "gitlab":
		return resolveGitLab(opt)
	default:
		return target{}, fmt.Errorf("--provider must be auto, github, or gitlab")
	}
}

func resolveGitHub(opt Options) (target, error) {
	repo := strings.TrimSpace(opt.Repo)
	if repo == "" {
		repo = strings.TrimSpace(opt.Env("GITHUB_REPOSITORY"))
	}
	repoPath, err := githubRepo(repo)
	if err != nil {
		return target{}, err
	}
	number := strings.TrimSpace(opt.PR)
	if number == "" {
		number = pullNumber(opt.Env)
	}
	if !positiveID(number) {
		return target{}, fmt.Errorf("GitHub needs a pull request number from --pr, GITHUB_EVENT_PATH, or GITHUB_REF")
	}
	token, err := cleanToken(opt.Env("GITHUB_TOKEN"))
	if err != nil {
		return target{}, fmt.Errorf("GitHub needs GITHUB_TOKEN with permission to write pull request comments")
	}
	fallback := "https://api.github.com"
	if v := strings.TrimSpace(opt.Env("GITHUB_API_URL")); v != "" {
		fallback = v
	}
	base, err := apiBase(opt, fallback)
	if err != nil {
		return target{}, err
	}
	return target{provider: "github", api: base, repo: repoPath, number: number, token: token}, nil
}

func resolveGitLab(opt Options) (target, error) {
	project := strings.TrimSpace(opt.Repo)
	if project == "" {
		project = strings.TrimSpace(opt.Env("CI_PROJECT_ID"))
	}
	projectPath, err := gitlabProject(project)
	if err != nil {
		return target{}, err
	}
	number := strings.TrimSpace(opt.MR)
	if number == "" {
		number = strings.TrimSpace(opt.Env("CI_MERGE_REQUEST_IID"))
	}
	if !positiveID(number) {
		return target{}, fmt.Errorf("GitLab needs a merge request iid from --mr or CI_MERGE_REQUEST_IID")
	}
	token, job, err := gitlabToken(opt.Env)
	if err != nil {
		return target{}, err
	}
	fallback := "https://gitlab.com/api/v4"
	if v := strings.TrimSpace(opt.Env("CI_API_V4_URL")); v != "" {
		fallback = v
	}
	base, err := apiBase(opt, fallback)
	if err != nil {
		return target{}, err
	}
	return target{provider: "gitlab", api: base, repo: projectPath, number: number, token: token, jobToken: job}, nil
}

func apiBase(opt Options, fallback string) (*url.URL, error) {
	raw := strings.TrimSpace(opt.APIBase)
	if raw == "" {
		raw = fallback
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return nil, fmt.Errorf("API URL must be an absolute https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("API URL must not contain a user, a query, or a fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !opt.AllowHTTP) {
		return nil, fmt.Errorf("API URL scheme must be https")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func gitlabToken(env func(string) string) (string, bool, error) {
	if tok := strings.TrimSpace(env("STAGEPROOF_GITLAB_TOKEN")); tok != "" {
		clean, err := cleanToken(tok)
		if err != nil {
			return "", false, fmt.Errorf("GitLab token was rejected")
		}
		return clean, false, nil
	}
	if tok := strings.TrimSpace(env("GITLAB_TOKEN")); tok != "" {
		clean, err := cleanToken(tok)
		if err != nil {
			return "", false, fmt.Errorf("GitLab token was rejected")
		}
		return clean, false, nil
	}
	if tok := strings.TrimSpace(env("CI_JOB_TOKEN")); tok != "" {
		clean, err := cleanToken(tok)
		if err != nil {
			return "", false, fmt.Errorf("GitLab token was rejected")
		}
		return clean, true, nil
	}
	return "", false, fmt.Errorf("GitLab needs GITLAB_TOKEN, STAGEPROOF_GITLAB_TOKEN, or CI_JOB_TOKEN with permission to write merge request notes")
}

func cleanToken(tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", fmt.Errorf("missing")
	}
	if len(tok) < minTokenLen || strings.ContainsAny(tok, "\r\n\x00") || strings.ContainsAny(tok, " \t") {
		return "", fmt.Errorf("rejected")
	}
	return tok, nil
}

func pullNumber(env func(string) string) string {
	if path := strings.TrimSpace(env("GITHUB_EVENT_PATH")); path != "" {
		if n := numberFromEvent(path); n != "" {
			return n
		}
	}
	m := pullRef.FindStringSubmatch(strings.TrimSpace(env("GITHUB_REF")))
	if m == nil {
		return ""
	}
	return m[1]
}

func numberFromEvent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return ""
	}
	var doc struct {
		Number      int64 `json:"number"`
		PullRequest struct {
			Number int64 `json:"number"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	n := doc.PullRequest.Number
	if n == 0 {
		n = doc.Number
	}
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func githubRepo(repo string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("GitHub needs a repository as owner/name from --repo or GITHUB_REPOSITORY")
	}
	if !safeSegment(owner) || !safeSegment(name) {
		return "", fmt.Errorf("GitHub repository must be owner/name")
	}
	return url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func gitlabProject(project string) (string, error) {
	if project == "" {
		return "", fmt.Errorf("GitLab needs a project id from --repo or CI_PROJECT_ID")
	}
	if positiveID(project) {
		return project, nil
	}
	parts := strings.Split(project, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("GitLab project must be a numeric id or a group/project path")
	}
	for _, part := range parts {
		if !safeSegment(part) {
			return "", fmt.Errorf("GitLab project must be a numeric id or a group/project path")
		}
	}
	return url.PathEscape(project), nil
}

func safeSegment(s string) bool {
	if s == "" || s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func positiveID(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

func truthy(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}

type note struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

func findMarker(ctx context.Context, client *http.Client, tgt target, marker string) (int64, int, []byte, error) {
	var next string
	switch tgt.provider {
	case "github":
		next = tgt.api.String() + "/repos/" + tgt.repo + "/issues/" + tgt.number + "/comments?per_page=100"
	default:
		next = tgt.api.String() + "/projects/" + tgt.repo + "/merge_requests/" + tgt.number + "/notes?per_page=100&sort=asc&page=1"
	}
	for page := 0; page < maxPages && next != ""; page++ {
		status, body, hdr, err := do(ctx, client, http.MethodGet, next, tgt, nil)
		if err != nil {
			return 0, status, body, err
		}
		var notes []note
		if json.Unmarshal(body, &notes) != nil {
			return 0, status, body, fmt.Errorf("comment list was not a JSON array")
		}
		for _, n := range notes {
			if n.ID > 0 && strings.Contains(n.Body, marker) {
				return n.ID, status, nil, nil
			}
		}
		next = nextPage(tgt, hdr, next)
	}
	return 0, 0, nil, nil
}

func nextPage(tgt target, hdr http.Header, current string) string {
	if tgt.provider == "github" {
		return githubNext(tgt.api, hdr.Get("Link"))
	}
	n := strings.TrimSpace(hdr.Get("X-Next-Page"))
	if n == "" || !positiveID(n) {
		return ""
	}
	u, err := url.Parse(current)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("page", n)
	u.RawQuery = q.Encode()
	if u.Host != tgt.api.Host || u.Scheme != tgt.api.Scheme {
		return ""
	}
	return u.String()
}

func githubNext(api *url.URL, link string) string {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end <= start {
			return ""
		}
		u, err := url.Parse(part[start+1 : end])
		if err != nil || u.Host != api.Host || u.Scheme != api.Scheme {
			return ""
		}
		if u.User != nil {
			return ""
		}
		return u.String()
	}
	return ""
}

func writePlan(tgt target, id int64) (method, rawURL, action string) {
	action = "Posted"
	if id > 0 {
		action = "Updated"
	}
	switch tgt.provider {
	case "github":
		if id > 0 {
			return http.MethodPatch, tgt.api.String() + "/repos/" + tgt.repo + "/issues/comments/" + strconv.FormatInt(id, 10), action
		}
		return http.MethodPost, tgt.api.String() + "/repos/" + tgt.repo + "/issues/" + tgt.number + "/comments", action
	default:
		base := tgt.api.String() + "/projects/" + tgt.repo + "/merge_requests/" + tgt.number + "/notes"
		if id > 0 {
			return http.MethodPut, base + "/" + strconv.FormatInt(id, 10), action
		}
		return http.MethodPost, base, action
	}
}

func place(tgt target) string {
	if tgt.provider == "github" {
		return "pull request #" + tgt.number
	}
	return "merge request !" + tgt.number
}

func doJSON(ctx context.Context, client *http.Client, method, rawURL string, tgt target, body string) (int, []byte, error) {
	payload, err := json.Marshal(struct {
		Body string `json:"body"`
	}{Body: body})
	if err != nil {
		return 0, nil, err
	}
	status, respBody, _, err := do(ctx, client, method, rawURL, tgt, payload)
	return status, respBody, err
}

func do(ctx context.Context, client *http.Client, method, rawURL string, tgt target, payload []byte) (int, []byte, http.Header, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", "stageproof")
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tgt.provider == "github" {
		req.Header.Set("Authorization", "Bearer "+tgt.token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	} else if tgt.jobToken {
		req.Header.Set("JOB-TOKEN", tgt.token)
	} else {
		req.Header.Set("PRIVATE-TOKEN", tgt.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCommentBytes))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, body, resp.Header, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, body, resp.Header, nil
}

func finish(opt Options, tgt target, status int, body []byte, err error) int {
	msg := scrub(err.Error(), tgt.token)
	detail := publicBody(body, tgt.token)
	if status == http.StatusForbidden {
		perm := "write pull request comments"
		if tgt.provider == "gitlab" {
			perm = "write merge request notes"
		}
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: the server returned HTTP 403. The token needs permission to %s. A fork pull request often has a read-only token.\n", perm)
		if detail != "" && !strings.Contains(detail, tgt.token) {
			_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: %s\n", detail)
		}
		if opt.BestEffort {
			return 0
		}
		return 2
	}
	if detail != "" {
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: %s: %s\n", msg, detail)
	} else {
		_, _ = fmt.Fprintf(opt.Stderr, "stageproof comment: %s\n", msg)
	}
	return 2
}

func publicBody(body []byte, token string) string {
	s := scrub(string(body), token)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

func scrub(msg, token string) string {
	if len(token) < minTokenLen {
		return msg
	}
	msg = strings.ReplaceAll(msg, token, "[redacted]")
	if esc := url.QueryEscape(token); esc != token {
		msg = strings.ReplaceAll(msg, esc, "[redacted]")
	}
	return msg
}

func refuseOffHost(allowHTTP bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		if len(via) >= 3 {
			return fmt.Errorf("stopped after 3 redirects")
		}
		first := via[0].URL
		if req.URL.Host != first.Host || req.URL.Scheme != first.Scheme {
			return fmt.Errorf("refused a redirect to a different host")
		}
		if req.URL.Scheme == "http" && !allowHTTP {
			return fmt.Errorf("refused a redirect to http")
		}
		return nil
	}
}
