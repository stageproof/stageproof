package check

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/k8sschema"
)

// loadRendered reads one YAML file, or every .yaml and .yml file under a
// directory. Paths are sorted so the same tree always parses in the same order.
func loadRendered(path string) ([]k8sschema.ManifestObject, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var files []string
	if !info.IsDir() {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
		default:
			return nil, fmt.Errorf("%s is not a .yaml or .yml file", path)
		}
		files = []string{path}
	} else if err := filepath.WalkDir(path, func(name string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".yaml", ".yml":
			files = append(files, name)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("%s contains no .yaml or .yml files", path)
	}
	var b strings.Builder
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		b.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("---\n")
	}
	objs, err := k8sschema.ParseManifestStream(b.String())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return objs, nil
}
