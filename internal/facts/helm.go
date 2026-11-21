package facts

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

const helmSecretType = "helm.sh/release.v1"

// helmRelease is the slice of a Helm release Secret this package keeps.
// Chart values live in other fields of that Secret and are not decoded into
// this struct.
type helmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Chart     struct {
		Metadata struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
	} `json:"chart"`
}

func chartsFromHelmSecrets(secrets []corev1.Secret) ([]Chart, error) {
	type pick struct {
		rev   int
		name  string
		chart Chart
	}
	best := map[string]pick{}
	for _, secret := range secrets {
		if secret.Type != helmSecretType {
			continue
		}
		raw, ok := secret.Data["release"]
		if !ok {
			return nil, fmt.Errorf("helm release secret %s/%s has no release field", secret.Namespace, secret.Name)
		}
		rel, err := decodeHelmRelease(raw)
		if err != nil {
			return nil, fmt.Errorf("helm release secret %s/%s could not be read", secret.Namespace, secret.Name)
		}
		release := secret.Labels["name"]
		if release == "" {
			release = rel.Name
		}
		if release == "" {
			release = secret.Name
		}
		chartName := rel.Chart.Metadata.Name
		if chartName == "" {
			chartName = release
		}
		if chartName == "" || rel.Chart.Metadata.Version == "" {
			return nil, fmt.Errorf("helm release secret %s/%s has no chart name and version", secret.Namespace, secret.Name)
		}
		ns := secret.Namespace
		if ns == "" {
			ns = rel.Namespace
		}
		rev := 0
		if text := secret.Labels["version"]; text != "" {
			n, err := strconv.Atoi(text)
			if err != nil {
				return nil, fmt.Errorf("helm release secret %s/%s has a revision that is not a number", secret.Namespace, secret.Name)
			}
			rev = n
		}
		key := ns + "\x00" + release
		chart := Chart{
			Source:     sourceHelm,
			Name:       chartName,
			Version:    rel.Chart.Metadata.Version,
			AppVersion: rel.Chart.Metadata.AppVersion,
			Release:    release,
			Namespace:  ns,
		}
		cur, ok := best[key]
		if !ok || rev > cur.rev || (rev == cur.rev && secret.Name > cur.name) {
			best[key] = pick{rev: rev, name: secret.Name, chart: chart}
		}
	}
	out := make([]Chart, 0, len(best))
	for _, item := range best {
		out = append(out, item.chart)
	}
	return out, nil
}

func decodeHelmRelease(raw []byte) (helmRelease, error) {
	var zero helmRelease
	payload, err := unpackHelm(raw)
	if err != nil {
		return zero, err
	}
	var rel helmRelease
	if err := json.Unmarshal(payload, &rel); err != nil {
		return zero, err
	}
	return rel, nil
}

// unpackHelm follows Helm's Secret encoding: base64, then gzip when the
// gzip header is present, then JSON. The decoded document is not returned to
// the caller except through helmRelease, which has no values field.
func unpackHelm(raw []byte) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		if len(raw) > 3 && bytes.Equal(raw[:3], []byte{0x1f, 0x8b, 0x08}) {
			b = raw
		} else {
			return nil, err
		}
	}
	if len(b) > 3 && bytes.Equal(b[:3], []byte{0x1f, 0x8b, 0x08}) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		defer func() { _ = r.Close() }()
		b, err = io.ReadAll(io.LimitReader(r, 32<<20))
		if err != nil {
			return nil, err
		}
	}
	return b, nil
}
