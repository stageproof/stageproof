// Package apiserver starts the Kubernetes API server published for a minor
// version and asks it whether an upgrade would be accepted.
package apiserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

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

// Start runs kube-apiserver and etcd for the newest published patch of version.
// version is a minor ("1.35") or a patch ("1.35.0"). binDir, when set, is a
// directory that already contains kube-apiserver, etcd, and kubectl. Nothing
// is downloaded in that case.
//
// Canceling ctx stops the processes. Stop is safe to call more than once.
func Start(ctx context.Context, version, binDir string) (*Server, error) {
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
	bin, binDirectory := findBinary(env.BinaryAssetsDirectory, "kube-apiserver")
	patch := version
	if bin != "" {
		patch = binaryVersion(bin)
		if patch == "" {
			patch = version
		}
	}
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

func findBinary(root, name string) (string, string) {
	direct := filepath.Join(root, name)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return direct, root
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", name))
	if len(matches) == 0 {
		return "", root
	}
	// The cache root holds one directory per patch. Prefer the newest name.
	best := matches[0]
	for _, m := range matches[1:] {
		if m > best {
			best = m
		}
	}
	return best, filepath.Dir(best)
}

var versionRE = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

func binaryVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	m := versionRE.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1]
}
