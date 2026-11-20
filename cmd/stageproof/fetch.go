package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/stageproof/stageproof/internal/apiserver"
)

func runFetchAPIServer(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fetch-apiserver", flag.ContinueOnError)
	k8s := fs.String("k8s", "", "Kubernetes minor, for example 1.35")
	out := fs.String("out", "", "Directory to write kube-apiserver, etcd, and kubectl")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *k8s == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: stageproof fetch-apiserver --k8s 1.35 --out <dir>")
		return 2
	}
	patch, err := apiserver.Fetch(ctx, *k8s, *out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch-apiserver: %v\n", err)
		return 2
	}
	fmt.Printf("%s\n%s\n", patch, *out)
	return 0
}
