package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/stageproof/stageproof/internal/facts"
	"k8s.io/client-go/tools/clientcmd"
)

func runFacts(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "export" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: stageproof facts export --kubeconfig <file> --context <name> --out <file> [--helm-releases]")
		return 2
	}
	fs := flag.NewFlagSet("facts export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kubeconfig := fs.String("kubeconfig", "", "Kubeconfig file. The default kubeconfig is not read.")
	contextName := fs.String("context", "", "Context in that kubeconfig")
	out := fs.String("out", "", "Path of the facts JSON file")
	helmReleases := fs.Bool("helm-releases", false, "Also read Helm release Secrets. This needs deploy/facts-helm-releases-clusterrole.yaml. Only the chart name, version, appVersion, release, and namespace are kept.")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*kubeconfig) == "" || strings.TrimSpace(*contextName) == "" || strings.TrimSpace(*out) == "" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: stageproof facts export --kubeconfig <file> --context <name> --out <file> [--helm-releases]")
		return 2
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: *contextName},
	).ClientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "facts export: %v\n", err)
		return 2
	}
	doc, err := facts.Export(ctx, cfg, facts.ExportOptions{HelmReleases: *helmReleases})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "facts export: %v\n", err)
		return 2
	}
	if err := facts.Write(*out, doc); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "facts export: %v\n", err)
		return 2
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s\n%s\n", doc.ServerVersion, *out)
	return 0
}

func loadClusterFacts(path string) (*facts.Document, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	doc, err := facts.Load(path)
	if err != nil {
		return nil, fmt.Errorf("cluster facts: %w", err)
	}
	return doc, nil
}
