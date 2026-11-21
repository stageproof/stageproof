package helm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChartAPIVersion reads apiVersion from Chart.yaml.
// A missing file is an empty version and a nil error. A file that does not
// parse returns an error.
func ChartAPIVersion(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("chart.yaml: %w", err)
	}
	return strings.TrimSpace(doc.APIVersion), nil
}
