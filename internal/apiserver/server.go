// Package apiserver starts the Kubernetes API server published for a minor
// version and asks it whether an upgrade would be accepted.
package apiserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// IndexURL is the controller-tools envtest index. It lists every published
// kube-apiserver, etcd, and kubectl archive and the SHA-512 of each archive.
const IndexURL = "https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/HEAD/envtest-releases.yaml"

// Server is a throwaway API server. Stop releases its processes.
type Server struct {
	env       *envtest.Environment
	cfg       *rest.Config
	dyn       dynamic.Interface
	mapper    meta.RESTMapper
	discovery discovery.DiscoveryInterface
	http      *http.Client
	version   string
	binDir    string
	once      sync.Once
	stopErr   error
}

func init() {
	ctrllog.SetLogger(logr.Discard())
}

// CacheRoot is where downloaded binaries are kept:
// <user cache>/stageproof/envtest/<patch>-<os>-<arch>/.
func CacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "stageproof", "envtest"), nil
}

// auditPolicy logs every request at Metadata. The body is not written.
const auditPolicy = `apiVersion: audit.k8s.io/v1
kind: Policy
rules:
- level: Metadata
`

type startOptions struct {
	// AuditLogPath turns on the API server audit log. The facts test uses it
	// to show the exporter's verbs. A normal proof does not set it.
	AuditLogPath string
}

// Start runs kube-apiserver and etcd for the newest published patch of version.
// version is a minor ("1.35") or a patch ("1.35.0"). binDir, when set, is a
// directory that already contains kube-apiserver, etcd, and kubectl. Nothing
// is downloaded in that case.
//
// Canceling ctx stops the processes. Stop is safe to call more than once.
func Start(ctx context.Context, version, binDir string) (*Server, error) {
	return start(ctx, version, binDir, startOptions{})
}

func start(ctx context.Context, version, binDir string, opt startOptions) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return nil, fmt.Errorf("kubernetes version is empty")
	}
	env := &envtest.Environment{
		DownloadBinaryAssetsIndexURL: IndexURL,
		CRDDirectoryPaths:            nil,
		ErrorIfCRDPathMissing:        false,
	}
	if binDir != "" {
		env.BinaryAssetsDirectory = binDir
	} else {
		root, err := CacheRoot()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		// Environment.DownloadBinaryAssets verifies the SHA-512 in the index
		// before it unpacks, and it skips the download when the binaries are
		// already in the cache.
		env.BinaryAssetsDirectory = root
		env.DownloadBinaryAssets = true
		env.DownloadBinaryAssetsVersion = version
	}
	if opt.AuditLogPath != "" {
		env.ControlPlaneStartTimeout = 60 * time.Second
		env.ControlPlaneStopTimeout = 30 * time.Second
		policy := filepath.Join(filepath.Dir(opt.AuditLogPath), "audit-policy.yaml")
		if err := os.WriteFile(policy, []byte(auditPolicy), 0o644); err != nil {
			return nil, err
		}
		// envtest already runs with RBAC. These flags add the audit log.
		env.ControlPlane.GetAPIServer().Configure().
			Set("audit-policy-file", policy).
			Set("audit-log-path", opt.AuditLogPath).
			Set("audit-log-format", "json").
			Set("audit-log-mode", "blocking")
	}
	cfg, err := env.Start()
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	info, err := disc.ServerVersion()
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	patch := versionFromGit(info.GitVersion)
	if patch == "" {
		patch = version
	}
	binDirectory := startedBinDir(env, binDir, patch)
	s := &Server{
		env: env, cfg: cfg, dyn: dyn, mapper: mapper, discovery: disc, http: httpClient,
		version: patch, binDir: binDirectory,
	}
	go func() {
		<-ctx.Done()
		_ = s.Stop()
	}()
	return s, nil
}

// Version is the patch the process reported, such as 1.35.0.
func (s *Server) Version() string {
	if s == nil {
		return ""
	}
	return s.version
}

// BinaryDir is the directory that holds kube-apiserver, etcd, and kubectl.
func (s *Server) BinaryDir() string { return s.binDir }

// ServedAPIs returns every group/version and group/version/Kind this API server
// serves, sorted and deduplicated. Subresources (names containing "/") are skipped.
func (s *Server) ServedAPIs() ([]string, error) {
	if s == nil || s.discovery == nil {
		return nil, fmt.Errorf("api server discovery is unavailable")
	}
	groups, lists, err := s.discovery.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			seen[v] = true
		}
	}
	for _, g := range groups {
		for _, v := range g.Versions {
			add(v.GroupVersion)
		}
	}
	for _, list := range lists {
		if list == nil || list.GroupVersion == "" {
			continue
		}
		add(list.GroupVersion)
		for _, r := range list.APIResources {
			if r.Kind == "" || strings.Contains(r.Name, "/") {
				continue
			}
			add(list.GroupVersion + "/" + r.Kind)
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// Stop ends kube-apiserver and etcd. A second call does nothing.
func (s *Server) Stop() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.env != nil {
			s.stopErr = s.env.Stop()
		}
	})
	return s.stopErr
}

// Fetch downloads the binaries for version into out. out then works as
// --api-server-dir. The patch version is returned.
func Fetch(ctx context.Context, version, out string) (string, error) {
	if out == "" {
		return "", fmt.Errorf("output directory is empty")
	}
	s, err := Start(ctx, version, "")
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Stop() }()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{"kube-apiserver", "etcd", "kubectl"} {
		src := filepath.Join(s.binDir, name)
		dst := filepath.Join(out, name)
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
	}
	return s.Version(), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// versionFromGit trims the leading v and the +build suffix from a GitVersion.
func versionFromGit(git string) string {
	v := strings.TrimPrefix(strings.TrimSpace(git), "v")
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// startedBinDir is the directory of the kube-apiserver process envtest started.
// A cache that also holds a newer patch must not win.
func startedBinDir(env *envtest.Environment, explicit, patch string) string {
	if env != nil {
		if p := env.ControlPlane.GetAPIServer().Path; p != "" {
			return filepath.Dir(p)
		}
	}
	if explicit != "" {
		return explicit
	}
	if env == nil || patch == "" {
		return ""
	}
	return filepath.Join(env.BinaryAssetsDirectory, patch+"-"+runtime.GOOS+"-"+runtime.GOARCH)
}
