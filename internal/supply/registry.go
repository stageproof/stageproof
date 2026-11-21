package supply

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type outcomeKind int

const (
	kindOK outcomeKind = iota
	kindMissing
	kindPlatform
	kindUnverifiable
	kindUnreachable
	kindSkip
)

type outcome struct {
	kind      outcomeKind
	platforms []string
}

type prober struct {
	ctx       context.Context
	timeout   time.Duration
	insecure  bool
	transport http.RoundTripper
	keychain  authn.Keychain
	hosts     *hostLimit
	slots     map[string]*slot
}

type slot struct {
	once sync.Once
	out  outcome
}

type hostLimit struct {
	mu   sync.Mutex
	sems map[string]chan struct{}
}

func newProber(ctx context.Context, opt Options, timeout time.Duration) *prober {
	kc := authn.DefaultKeychain
	slots := map[string]*slot{}
	return &prober{
		ctx:       ctx,
		timeout:   timeout,
		insecure:  opt.Insecure,
		transport: opt.RoundTripper,
		keychain:  kc,
		hosts:     &hostLimit{sems: map[string]chan struct{}{}},
		slots:     slots,
	}
}

func (p *prober) probeAll(groups []group) []outcome {
	for _, g := range groups {
		if _, ok := p.slots[g.after]; !ok {
			p.slots[g.after] = &slot{}
		}
	}
	var wg sync.WaitGroup
	for _, g := range groups {
		wg.Add(1)
		go func(image string) {
			defer wg.Done()
			p.slots[image].once.Do(func() {
				p.slots[image].out = p.probe(image)
			})
		}(g.after)
	}
	wg.Wait()
	out := make([]outcome, len(groups))
	for i, g := range groups {
		out[i] = p.slots[g.after].out
	}
	return out
}

func (p *prober) probe(image string) outcome {
	if err := p.ctx.Err(); err != nil {
		return outcome{kind: kindUnreachable}
	}
	ref, err := parseRef(image, p.insecure)
	if err != nil {
		return outcome{kind: kindSkip}
	}
	host := ref.Context().RegistryStr()
	if err := p.hosts.acquire(p.ctx, host); err != nil {
		return outcome{kind: kindUnreachable}
	}
	defer p.hosts.release(host)

	reqCtx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	desc, err := remote.Head(ref, p.remoteOpts(reqCtx)...)
	if err != nil {
		return outcome{kind: classify(err)}
	}
	if !isIndex(desc.MediaType) {
		return outcome{kind: kindOK}
	}
	idx, err := remote.Index(ref, p.remoteOpts(reqCtx)...)
	if err != nil {
		return outcome{kind: classify(err)}
	}
	mf, err := idx.IndexManifest()
	if err != nil {
		return outcome{kind: kindUnreachable}
	}
	return outcome{kind: kindPlatform, platforms: platformsOf(mf)}
}

func (p *prober) remoteOpts(ctx context.Context) []remote.Option {
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(p.keychain),
	}
	if p.transport != nil {
		opts = append(opts, remote.WithTransport(p.transport))
	}
	return opts
}

func parseRef(image string, insecure bool) (name.Reference, error) {
	var opts []name.Option
	if insecure {
		opts = append(opts, name.Insecure)
	}
	return name.ParseReference(image, opts...)
}

func repository(image string, insecure bool) (string, bool) {
	ref, err := parseRef(image, insecure)
	if err != nil {
		return "", false
	}
	return ref.Context().Name(), true
}

func isIndex(mt types.MediaType) bool {
	switch mt {
	case types.OCIImageIndex, types.DockerManifestList:
		return true
	default:
		return false
	}
}

func platformsOf(mf *v1.IndexManifest) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range mf.Manifests {
		if d.Platform == nil || d.Platform.OS == "" || d.Platform.Architecture == "" {
			continue
		}
		p := d.Platform.OS + "/" + d.Platform.Architecture
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func classify(err error) outcomeKind {
	if err == nil {
		return kindOK
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return kindUnreachable
	}
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusNotFound:
			return kindMissing
		case http.StatusUnauthorized, http.StatusForbidden:
			return kindUnverifiable
		case http.StatusTooManyRequests:
			return kindUnreachable
		}
		if te.StatusCode >= 500 {
			return kindUnreachable
		}
		for _, d := range te.Errors {
			code := strings.ToUpper(string(d.Code))
			if code != "MANIFEST_UNKNOWN" && code != "NAME_UNKNOWN" {
				continue
			}
			if te.StatusCode == http.StatusUnauthorized || te.StatusCode == http.StatusForbidden {
				return kindUnverifiable
			}
			return kindMissing
		}
		return kindUnreachable
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return kindUnreachable
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return kindUnreachable
	}
	return kindUnreachable
}

func (h *hostLimit) acquire(ctx context.Context, host string) error {
	h.mu.Lock()
	ch, ok := h.sems[host]
	if !ok {
		ch = make(chan struct{}, maxPerHost)
		h.sems[host] = ch
	}
	h.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hostLimit) release(host string) {
	h.mu.Lock()
	ch := h.sems[host]
	h.mu.Unlock()
	if ch != nil {
		<-ch
	}
}
