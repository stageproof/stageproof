package report

import (
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/rules"
	"github.com/stageproof/stageproof/internal/verdict"
)

const sarifSchema = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"

// WriteSARIF writes one SARIF 2.1.0 log for these reports.
// The message is the finding message or the skip reason. Detail is left out.
func WriteSARIF(w io.Writer, reports []*verdict.Report, toolVersion, verdictName string, exitCode int) error {
	log := buildSARIF(reports, toolVersion, verdictName, exitCode)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Invocations []sarifInvocation `json:"invocations"`
	Results     []sarifResult     `json:"results"`
	Properties  map[string]any    `json:"properties,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string        `json:"id"`
	ShortDescription     sarifMessage  `json:"shortDescription"`
	FullDescription      *sarifMessage `json:"fullDescription,omitempty"`
	Help                 *sarifMessage `json:"help,omitempty"`
	DefaultConfiguration *sarifDefault `json:"defaultConfiguration,omitempty"`
}

type sarifDefault struct {
	Level string `json:"level"`
}

type sarifInvocation struct {
	ExecutionSuccessful bool `json:"executionSuccessful"`
	ExitCode            int  `json:"exitCode"`
}

type sarifResult struct {
	RuleID       string             `json:"ruleId"`
	RuleIndex    int                `json:"ruleIndex"`
	Level        string             `json:"level"`
	Message      sarifMessage       `json:"message"`
	Locations    []sarifLocation    `json:"locations,omitempty"`
	Suppressions []sarifSuppression `json:"suppressions,omitempty"`
	Properties   map[string]any     `json:"properties,omitempty"`
}

type sarifSuppression struct {
	Kind          string `json:"kind"`
	Justification string `json:"justification,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation *sarifPhysical `json:"physicalLocation,omitempty"`
	LogicalLocations []sarifLogical `json:"logicalLocations,omitempty"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifLogical struct {
	FullyQualifiedName string `json:"fullyQualifiedName"`
	Kind               string `json:"kind,omitempty"`
}

type sarifItem struct {
	id          string
	level       string
	message     string
	file        string
	resource    string
	props       map[string]any
	rule        sarifRule
	suppression string
}

func buildSARIF(reports []*verdict.Report, toolVersion, verdictName string, exitCode int) sarifLog {
	var items []sarifItem
	for _, rep := range reports {
		if rep == nil {
			continue
		}
		for _, f := range rep.Findings {
			items = append(items, findingItem(rep, f))
		}
		for _, s := range rep.Skipped {
			if strings.TrimSpace(s.Code) == "" && strings.TrimSpace(s.Reason) == "" {
				continue
			}
			items = append(items, skipItem(rep, s))
		}
	}
	rulesByID := map[string]sarifRule{}
	var ids []string
	for _, item := range items {
		if _, ok := rulesByID[item.id]; ok {
			continue
		}
		rulesByID[item.id] = item.rule
		ids = append(ids, item.id)
	}
	sort.Strings(ids)
	ruleList := make([]sarifRule, len(ids))
	index := map[string]int{}
	for i, id := range ids {
		ruleList[i] = rulesByID[id]
		index[id] = i
	}
	results := make([]sarifResult, len(items))
	for i, item := range items {
		res := sarifResult{
			RuleID:     item.id,
			RuleIndex:  index[item.id],
			Level:      item.level,
			Message:    sarifMessage{Text: item.message},
			Properties: item.props,
		}
		if loc, ok := itemLocation(item); ok {
			res.Locations = []sarifLocation{loc}
		}
		if item.suppression != "" {
			res.Suppressions = []sarifSuppression{{
				Kind:          "external",
				Justification: item.suppression,
			}}
		}
		results[i] = res
	}
	if toolVersion == "" {
		toolVersion = "dev"
	}
	props := map[string]any{}
	if verdictName != "" {
		props["verdict"] = verdictName
	}
	return sarifLog{
		Schema:  sarifSchema,
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           "stageproof",
				Version:        toolVersion,
				InformationURI: "https://stageproof.github.io/stageproof/",
				Rules:          ruleList,
			}},
			Invocations: []sarifInvocation{{
				ExecutionSuccessful: exitCode == 0,
				ExitCode:            exitCode,
			}},
			Results:    results,
			Properties: props,
		}},
	}
}

func findingItem(rep *verdict.Report, f verdict.Finding) sarifItem {
	msg := oneLine(f.Message)
	if msg == "" {
		msg = oneLine(f.Resource)
	}
	if msg == "" {
		msg = f.RuleID
	}
	props := map[string]any{
		"severity": string(f.Severity),
	}
	if rep.BaseRef != "" {
		props["base_ref"] = rep.BaseRef
	}
	if rep.HeadRef != "" {
		props["head_ref"] = rep.HeadRef
	}
	if f.Proof != "" {
		props["proof"] = f.Proof
	}
	if f.Verified != nil {
		props["verified"] = *f.Verified
	}
	item := sarifItem{
		id:       f.RuleID,
		level:    sarifLevel(f.Severity),
		message:  msg,
		file:     f.File,
		resource: f.Resource,
		props:    props,
		rule:     ruleForFinding(f),
	}
	if f.Ignored {
		item.suppression = "Ignored until " + f.IgnoreExpires + ": " + oneLine(f.IgnoreReason)
	}
	return item
}

func skipItem(rep *verdict.Report, s verdict.SkipNotice) sarifItem {
	id := strings.TrimSpace(s.Code)
	if id == "" {
		id = "not_checked"
	}
	msg := oneLine(s.Reason)
	if msg == "" {
		msg = id
	}
	props := map[string]any{"severity": "info"}
	if rep.BaseRef != "" {
		props["base_ref"] = rep.BaseRef
	}
	if rep.HeadRef != "" {
		props["head_ref"] = rep.HeadRef
	}
	help := oneLine(s.Remediation)
	rule := sarifRule{
		ID:                   id,
		ShortDescription:     sarifMessage{Text: id},
		FullDescription:      &sarifMessage{Text: msg},
		DefaultConfiguration: &sarifDefault{Level: "note"},
	}
	if help != "" {
		rule.Help = &sarifMessage{Text: help}
	}
	return sarifItem{
		id:      id,
		level:   "note",
		message: msg,
		props:   props,
		rule:    rule,
	}
}

func ruleForFinding(f verdict.Finding) sarifRule {
	level := sarifLevel(f.Severity)
	rule := sarifRule{
		ID:                   f.RuleID,
		ShortDescription:     sarifMessage{Text: f.RuleID},
		DefaultConfiguration: &sarifDefault{Level: level},
	}
	if known, ok := rules.Lookup(f.RuleID); ok {
		if known.Title != "" {
			rule.ShortDescription.Text = rules.RenderPlain(known.Title)
		}
		if known.Explanation != "" {
			rule.FullDescription = &sarifMessage{Text: rules.RenderPlain(known.Explanation)}
		}
		if known.Fix != "" {
			rule.Help = &sarifMessage{Text: rules.RenderPlain(known.Fix)}
		}
		rule.DefaultConfiguration.Level = sarifLevel(known.Severity)
	}
	return rule
}

func sarifLevel(sev verdict.Severity) string {
	switch sev {
	case verdict.SeverityBlocker:
		return "error"
	case verdict.SeverityWarning:
		return "warning"
	default:
		return "note"
	}
}

func itemLocation(item sarifItem) (sarifLocation, bool) {
	var loc sarifLocation
	ok := false
	if item.file != "" {
		loc.PhysicalLocation = &sarifPhysical{ArtifactLocation: sarifArtifact{URI: sarifURI(item.file)}}
		ok = true
	}
	if item.resource != "" {
		loc.LogicalLocations = []sarifLogical{{
			FullyQualifiedName: item.resource,
			Kind:               "resource",
		}}
		ok = true
	}
	return loc, ok
}

func sarifURI(path string) string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
