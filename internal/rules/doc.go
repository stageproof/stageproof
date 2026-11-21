package rules

import (
	"fmt"
	"os"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

// DocsTables renders one findings table per group, in registry order.
func DocsTables() map[string]string {
	by := map[string][]Rule{}
	var order []string
	for _, rule := range Rules() {
		if _, ok := by[rule.Group]; !ok {
			order = append(order, rule.Group)
		}
		by[rule.Group] = append(by[rule.Group], rule)
	}
	out := map[string]string{}
	for _, group := range order {
		out[group] = tableHTML(by[group])
	}
	return out
}

// SyncDocs replaces each <!-- rules:NAME --> block in path with the table
// generated from the registry.
func SyncDocs(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	for group, table := range DocsTables() {
		start := "<!-- rules:" + group + " -->"
		end := "<!-- /rules:" + group + " -->"
		if strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
			return fmt.Errorf("docs marker %s must appear once", group)
		}
		i := strings.Index(text, start)
		j := strings.Index(text, end)
		if j < i {
			return fmt.Errorf("docs marker %s is out of order", group)
		}
		text = text[:i+len(start)] + "\n" + table + "\n" + text[j:]
	}
	if text == string(raw) {
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func tableHTML(rules []Rule) string {
	var b strings.Builder
	b.WriteString(`<div class="doc-table-wrap"><table class="doc-table">
  <thead><tr><th>Rule</th><th>Level</th><th>Meaning</th><th>What to do</th></tr></thead>
  <tbody>
`)
	for _, rule := range rules {
		class, label := badge(rule.Severity)
		fmt.Fprintf(&b, "    <tr><td><code>%s</code></td><td><span class=\"verdict-badge %s\">%s</span></td><td>%s</td><td>%s</td></tr>\n",
			escapeHTML(rule.ID), class, label, renderHTML(rule.Explanation), renderHTML(rule.Fix))
	}
	b.WriteString(`  </tbody>
</table></div>`)
	return b.String()
}

func badge(sev verdict.Severity) (class, label string) {
	switch sev {
	case verdict.SeverityBlocker:
		return "bad", "ERROR"
	case verdict.SeverityInfo:
		return "slate", "INFO"
	default:
		return "warn", "WARN"
	}
}

// Explain prints one rule for the terminal. The bool is false when id is unknown.
func Explain(id string) (string, bool) {
	rule, ok := Lookup(id)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n%s\n\n%s\n\nWhat to do\n%s\n",
		rule.ID, rule.Title, levelWord(rule.Severity), renderPlain(rule.Explanation), renderPlain(rule.Fix))
	return b.String(), true
}

func levelWord(sev verdict.Severity) string {
	switch sev {
	case verdict.SeverityBlocker:
		return "ERROR"
	case verdict.SeverityInfo:
		return "INFO"
	default:
		return "WARN"
	}
}

func renderHTML(s string) string {
	var b strings.Builder
	for _, part := range scan(s) {
		switch part.kind {
		case partText:
			b.WriteString(escapeHTML(part.text))
		case partCode:
			b.WriteString("<code>")
			b.WriteString(escapeHTML(part.text))
			b.WriteString("</code>")
		case partLink:
			href := part.href
			if !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "https://") {
				b.WriteString(escapeHTML(part.text))
				continue
			}
			fmt.Fprintf(&b, `<a href="%s">%s</a>`, escapeHTML(href), escapeHTML(part.text))
		}
	}
	return b.String()
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;").Replace(s)
}

func renderPlain(s string) string {
	var b strings.Builder
	for _, part := range scan(s) {
		b.WriteString(part.text)
		if part.kind == partLink && strings.HasPrefix(part.href, "https://") {
			b.WriteString(" (")
			b.WriteString(part.href)
			b.WriteString(")")
		}
	}
	return b.String()
}

type partKind int

const (
	partText partKind = iota
	partCode
	partLink
)

type part struct {
	kind partKind
	text string
	href string
}

func scan(s string) []part {
	var out []part
	for len(s) > 0 {
		code := strings.Index(s, "%%")
		link := strings.Index(s, "[[")
		next, kind := -1, partText
		if code >= 0 && (link < 0 || code < link) {
			next, kind = code, partCode
		} else if link >= 0 {
			next, kind = link, partLink
		}
		if next < 0 {
			out = append(out, part{kind: partText, text: s})
			break
		}
		if next > 0 {
			out = append(out, part{kind: partText, text: s[:next]})
		}
		if kind == partCode {
			rest := s[next+2:]
			end := strings.Index(rest, "%%")
			if end < 0 {
				out = append(out, part{kind: partText, text: s[next:]})
				break
			}
			out = append(out, part{kind: partCode, text: rest[:end]})
			s = rest[end+2:]
			continue
		}
		rest := s[next+2:]
		end := strings.Index(rest, "]]")
		if end < 0 {
			out = append(out, part{kind: partText, text: s[next:]})
			break
		}
		body := rest[:end]
		label, href, ok := strings.Cut(body, "|")
		if !ok {
			out = append(out, part{kind: partText, text: body})
		} else {
			out = append(out, part{kind: partLink, text: label, href: href})
		}
		s = rest[end+2:]
	}
	return out
}
