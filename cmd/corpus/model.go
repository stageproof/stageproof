package main

import "time"

type incidentCase struct {
	ID      string `yaml:"id"`
	Summary string `yaml:"summary"`
	K8s     string `yaml:"k8s"`
	Values  string `yaml:"values"`
	Expect  struct {
		Verdict string   `yaml:"verdict"`
		Rules   []string `yaml:"rules"`
	} `yaml:"expect"`
	ControlExpect struct {
		Verdict string   `yaml:"verdict"`
		Rules   []string `yaml:"rules"`
	} `yaml:"control_expect"`
	Status string `yaml:"status"`
	Needs  string `yaml:"needs"`
	Source string `yaml:"source"`
}

type pair struct {
	ID    string `yaml:"id"`
	Repo  string `yaml:"repo"`
	Chart string `yaml:"chart"`
	From  string `yaml:"from"`
	To    string `yaml:"to"`
	Kind  string `yaml:"kind"`
	PR    bool   `yaml:"pr"`
}

type pairsFile struct {
	Pairs []pair `yaml:"pairs"`
}

type archive struct {
	Repo    string `yaml:"repo"`
	Chart   string `yaml:"chart"`
	Version string `yaml:"version"`
	SHA256  string `yaml:"sha256"`
}

type lockFile struct {
	Archives []archive `yaml:"archives"`
}

type label struct {
	Case     string `yaml:"case"`
	Truth    string `yaml:"truth"`
	Evidence string `yaml:"evidence"`
	By       string `yaml:"by"`
	Date     string `yaml:"date"`
}

type labelsFile struct {
	Labels []label `yaml:"labels"`
}

type caseResult struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Status         string   `json:"status,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	Rules          []string `json:"rules,omitempty"`
	OK             bool     `json:"ok"`
	ControlVerdict string   `json:"control_verdict,omitempty"`
	ControlOK      bool     `json:"control_ok"`
	Labeled        string   `json:"labeled,omitempty"`
	Seconds        float64  `json:"seconds"`
	Note           string   `json:"note,omitempty"`
}

type summary struct {
	IncidentsCaught  int     `json:"incidents_caught"`
	IncidentsActive  int     `json:"incidents_active"`
	ControlsPassed   int     `json:"controls_passed"`
	Controls         int     `json:"controls"`
	WrongBlocks      int     `json:"wrong_blocks"`
	LabeledClean     int     `json:"labeled_clean"`
	P50Seconds       float64 `json:"p50_seconds"`
	P95Seconds       float64 `json:"p95_seconds"`
	ReplayCases      int     `json:"replay_cases,omitempty"`
	ReplayChecked    int     `json:"replay_checked,omitempty"`
	ReplayPassed     int     `json:"replay_passed,omitempty"`
	ReplayBlocked    int     `json:"replay_blocked,omitempty"`
	ReplayNotChecked int     `json:"replay_not_checked,omitempty"`
	ReplayProven     int     `json:"replay_proven,omitempty"`
	ReplayErrors     int     `json:"replay_errors,omitempty"`
}

type resultsFile struct {
	Incidents []caseResult `json:"incidents"`
	Pairs     []caseResult `json:"pairs"`
	Replay    []caseResult `json:"replay,omitempty"`
	Summary   summary      `json:"summary"`
}

type baselineFile struct {
	Summary      summary  `json:"summary"`
	BlockedClean []string `json:"blocked_clean"`
	Recorded     string   `json:"recorded"`
}

func nowDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
