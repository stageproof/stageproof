// Package website checks the static product site in web/.
package website

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const chartIndexURL = "https://stageproof.github.io/index/"

var (
	pages = []string{"index.html", "docs.html", "architecture.html", "security.html", "roadmap.html"}

	// The index site (stageproof/index, internal/site/header.go) copies this
	// header and the footer links, and its CI compares them with the live
	// pages. Change both together.
	wantNav = []navLink{
		{"Overview", "index.html"},
		{"Docs", "docs.html"},
		{"Charts", chartIndexURL},
		{"Check values", chartIndexURL + "values/"},
		{"Security", "security.html"},
		{"Roadmap", "roadmap.html"},
	}

	navBlock    = regexp.MustCompile(`(?s)<nav class="nav-links"[^>]*>(.*?)</nav>`)
	footerBlock = regexp.MustCompile(`(?s)<footer class="footer">.*?</footer>`)
	homeCode    = regexp.MustCompile(`(?s)<pre id="code-[a-z]+">(.*?)</pre>`)
	anchor      = regexp.MustCompile(`(?s)<a\s([^>]*)>(.*?)</a>`)
	hrefAttr    = regexp.MustCompile(`href="([^"]*)"`)
	tags        = regexp.MustCompile(`<[^>]+>`)
)

type navLink struct{ label, href string }

func webDir() string { return filepath.Join("..", "..", "web") }

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(webDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEveryPageHasTheSameNav(t *testing.T) {
	for _, page := range pages {
		html := read(t, page)
		m := navBlock.FindStringSubmatch(html)
		if m == nil {
			t.Fatalf("%s: no nav-links block", page)
		}
		var got []navLink
		active := 0
		for _, a := range anchor.FindAllStringSubmatch(m[1], -1) {
			h := hrefAttr.FindStringSubmatch(a[1])
			if h == nil {
				t.Fatalf("%s: nav link without href: %s", page, a[0])
			}
			got = append(got, navLink{strings.TrimSpace(tags.ReplaceAllString(a[2], "")), h[1]})
			if strings.Contains(a[1], `class="active"`) {
				active++
				if h[1] != page {
					t.Errorf("%s: active link is %s", page, h[1])
				}
			}
		}
		if len(got) != len(wantNav) {
			t.Fatalf("%s: nav = %v, want %v", page, got, wantNav)
		}
		for i := range wantNav {
			if got[i] != wantNav[i] {
				t.Errorf("%s: nav[%d] = %v, want %v", page, i, got[i], wantNav[i])
			}
		}
		inNav := false
		for _, link := range wantNav {
			inNav = inNav || link.href == page
		}
		if want := map[bool]int{true: 1, false: 0}[inNav]; active != want {
			t.Errorf("%s: %d active nav links, want %d", page, active, want)
		}
		for _, want := range []string{`id="theme-toggle"`, `id="nav-toggle"`} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: missing %s", page, want)
			}
		}
	}
}

func TestEveryPageHasTheSameFooter(t *testing.T) {
	want := ""
	for _, page := range pages {
		footer := footerBlock.FindString(read(t, page))
		if footer == "" {
			t.Fatalf("%s: no footer", page)
		}
		if want == "" {
			want = footer
			continue
		}
		if footer != want {
			t.Errorf("%s: footer differs from %s", page, pages[0])
		}
	}
	for _, href := range []string{chartIndexURL, chartIndexURL + "values/", chartIndexURL + "api/", "architecture.html"} {
		if !strings.Contains(want, `href="`+href+`"`) {
			t.Errorf("footer does not link %s", href)
		}
	}
}

// The usage tabs explain a command above its box. The box holds only what
// a reader copies.
func TestHomeCodeBoxesHoldOnlyCode(t *testing.T) {
	blocks := homeCode.FindAllStringSubmatch(read(t, "index.html"), -1)
	if len(blocks) < 5 {
		t.Fatalf("found %d usage code blocks, want 5", len(blocks))
	}
	for _, b := range blocks {
		for _, line := range strings.Split(b[1], "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.Contains(line, " # ") {
				t.Errorf("usage code block has a prose comment: %q", line)
			}
		}
	}
}

func TestFontsAreSelfHosted(t *testing.T) {
	err := filepath.WalkDir(webDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".woff2") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, host := range []string{"fonts.googleapis.com", "fonts.gstatic.com"} {
			if strings.Contains(string(b), host) {
				t.Errorf("%s references %s", path, host)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	css := read(t, filepath.Join("assets", "styles.css"))
	for _, font := range []string{"inter-latin", "space-grotesk-latin", "jetbrains-mono-latin"} {
		if !strings.Contains(css, `url("fonts/`+font+`.woff2")`) {
			t.Errorf("styles.css does not load %s", font)
		}
		info, err := os.Stat(filepath.Join(webDir(), "assets", "fonts", font+".woff2"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() < 10000 {
			t.Errorf("%s.woff2 is %d bytes", font, info.Size())
		}
	}
	if !strings.Contains(read(t, filepath.Join("assets", "fonts", "LICENSE.txt")), "SIL OPEN FONT LICENSE") {
		t.Error("fonts/LICENSE.txt does not carry the OFL")
	}
	for _, page := range pages {
		if !strings.Contains(read(t, page), `href="assets/fonts/inter-latin.woff2" as="font"`) {
			t.Errorf("%s does not preload Inter", page)
		}
	}
}

func TestThemeKeyIsShared(t *testing.T) {
	// stageproof.github.io/index/ reads the same key from the same origin.
	if !strings.Contains(read(t, filepath.Join("assets", "app.js")), `"stageproof-theme"`) {
		t.Error("app.js does not use the stageproof-theme key")
	}
	for _, page := range pages {
		if !strings.Contains(read(t, page), `localStorage.getItem("stageproof-theme")`) {
			t.Errorf("%s does not paint the stored theme before load", page)
		}
	}
}
