package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// briefSection is one group in the summary at the top of a report.
// The titles and their order are fixed. An empty group is left out.
type briefSection struct {
	title string
	lines []briefLine
}

type briefLine struct {
	level string
	text  string
}

// briefTitles is the summary a person reads before the check list.
// Capacity and every other rule stay in the finding list.
var briefTitles = []string{
	"Deletes",
	"CRDs",
	"Privilege",
	"Exposure",
	"Values",
	"Restarts",
	"Not checked",
}

func briefSections(r *verdict.Report) []briefSection {
	if r == nil {
		return nil
	}
	var groups [7][]briefLine
	for _, f := range r.Findings {
		cat := findingCategory(f.RuleID)
		if cat > 5 {
			continue
		}
		groups[cat] = append(groups[cat], briefLine{level: levelWord(f.Severity), text: briefText(f)})
	}
	for _, s := range r.Skipped {
		text := strings.TrimSpace(s.Code)
		reason := oneLine(s.Reason)
		if reason != "" {
			if text != "" {
				text += "  " + reason
			} else {
				text = reason
			}
		}
		if text == "" {
			continue
		}
		groups[6] = append(groups[6], briefLine{text: text})
	}
	var out []briefSection
	for i, title := range briefTitles {
		if len(groups[i]) == 0 {
			continue
		}
		out = append(out, briefSection{title: title, lines: groups[i]})
	}
	return out
}

func briefText(f verdict.Finding) string {
	text := oneLine(f.Message)
	if text == "" {
		text = oneLine(f.Resource)
	}
	if text == "" {
		text = f.RuleID
	}
	if f.Ignored {
		text += " Ignored until " + f.IgnoreExpires + ": " + oneLine(f.IgnoreReason)
	}
	return text
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func levelWord(sev verdict.Severity) string {
	switch sev {
	case verdict.SeverityBlocker:
		return "ERROR"
	case verdict.SeverityWarning:
		return "WARN"
	default:
		return "INFO"
	}
}

func writeBrief(w io.Writer, r *verdict.Report, color bool) {
	sections := briefSections(r)
	if len(sections) == 0 {
		return
	}
	for i, sec := range sections {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w, sec.title)
		for _, line := range sec.lines {
			_, _ = fmt.Fprintln(w, formatBriefLine(color, line))
		}
	}
	_, _ = fmt.Fprintln(w)
}

func formatBriefLine(color bool, line briefLine) string {
	if line.level == "" {
		return "  " + line.text
	}
	code := ansiCyan
	switch line.level {
	case "ERROR":
		code = ansiRed
	case "WARN":
		code = ansiYellow
	}
	return "  " + paint(color, code, line.level) + "  " + line.text
}

func briefIcon(title string) string {
	switch title {
	case "Deletes":
		return "🗑️"
	case "CRDs":
		return "📜"
	case "Privilege":
		return "🔐"
	case "Exposure":
		return "🌐"
	case "Values":
		return "⚙️"
	case "Restarts":
		return "🔄"
	case "Not checked":
		return "⏭️"
	default:
		return "•"
	}
}

func briefLevelMarkdown(level string) string {
	switch level {
	case "ERROR":
		return "❌ **ERROR**"
	case "WARN":
		return "⚠️ **WARN**"
	case "INFO":
		return "ℹ️ **INFO**"
	default:
		return "**" + level + "**"
	}
}

func collapseBriefSection(sec briefSection) bool {
	if sec.title == "Restarts" {
		for _, line := range sec.lines {
			if line.level == "ERROR" || line.level == "WARN" {
				return false
			}
		}
		return true
	}
	if sec.title == "Values" && len(sec.lines) > 5 {
		for _, line := range sec.lines {
			if line.level == "ERROR" {
				return false
			}
		}
		return true
	}
	return false
}

func writeBriefMarkdown(w io.Writer, r *verdict.Report) {
	sections := briefSections(r)
	if len(sections) == 0 {
		return
	}
	for _, sec := range sections {
		icon := briefIcon(sec.title)
		if collapseBriefSection(sec) {
			_, _ = fmt.Fprintf(w, "<details>\n<summary>%s <b>%s (%d)</b></summary>\n\n", icon, sec.title, len(sec.lines))
			for _, line := range sec.lines {
				if line.level == "" {
					_, _ = fmt.Fprintf(w, "- %s\n", line.text)
					continue
				}
				_, _ = fmt.Fprintf(w, "- %s %s\n", briefLevelMarkdown(line.level), line.text)
			}
			_, _ = fmt.Fprint(w, "\n</details>\n\n")
			continue
		}
		_, _ = fmt.Fprintf(w, "### %s %s\n\n", icon, sec.title)
		for _, line := range sec.lines {
			if line.level == "" {
				_, _ = fmt.Fprintf(w, "- %s\n", line.text)
				continue
			}
			_, _ = fmt.Fprintf(w, "- %s %s\n", briefLevelMarkdown(line.level), line.text)
		}
		_, _ = fmt.Fprintln(w)
	}
}
