package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func compare(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: corpus compare results.json baseline.json")
	}
	var got resultsFile
	var base baselineFile
	if err := readJSON(args[0], &got); err != nil {
		return err
	}
	if err := readJSON(args[1], &base); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, id := range base.BlockedClean {
		known[id] = true
	}
	var problems []string
	for _, c := range got.Incidents {
		if c.Status == "active" && !c.OK {
			problems = append(problems, "missed active incident "+c.ID)
		}
		if c.Status != "unreproduced" && !c.ControlOK {
			problems = append(problems, "control blocked or wrong "+c.ID)
		}
	}
	for _, c := range got.Pairs {
		if c.Labeled == "clean" && c.Verdict == "BLOCKED" && !known[c.ID] {
			problems = append(problems, "labeled-clean newly blocked "+c.ID)
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		return fmt.Errorf("%d corpus regressions", len(problems))
	}
	fmt.Println("corpus compare: no missed incident, no failed control, no new clean block")
	return nil
}

func readJSON(path string, dest any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}
