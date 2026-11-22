package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "compare":
		err = compare(os.Args[2:])
	case "fetch":
		err = fetch(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `stageproof corpus

  go run ./cmd/corpus run --set incidents|pairs|replay|all [--api-server auto|off|require] --out results.json
  go run ./cmd/corpus compare results.json corpus/baseline.json
  go run ./cmd/corpus fetch

fetch writes pairs/pairs.yaml and pairs/pairs.lock.
replay reads corpus/replay/cases.jsonl, fetches those public Git commits, and prints aggregate counts.
--set all runs incidents and pairs.
`)
}
