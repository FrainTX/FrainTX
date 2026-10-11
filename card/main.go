// Command card draws the profile statistics card (dark and light SVG) from
// the GitHub GraphQL API. It runs daily in GitHub Actions; no third-party
// card service is involved, so the card never shows "rate limited".
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const query = `query($login: String!) {
  user(login: $login) {
    login
    contributionsCollection {
      totalCommitContributions
      restrictedContributionsCount
      totalPullRequestContributions
      totalIssueContributions
      totalRepositoriesWithContributedCommits
      contributionCalendar {
        totalContributions
        weeks { contributionDays { contributionCount } }
      }
    }
    repositories(first: 100, ownerAffiliations: OWNER, isFork: false, privacy: PUBLIC) {
      totalCount
      nodes {
        stargazerCount
        forkCount
        languages(first: 10, orderBy: {field: SIZE, direction: DESC}) {
          edges { size node { name } }
        }
      }
    }
  }
}`

type data struct {
	Login   string
	Total   int // contributions in the last year
	Days    int // days with at least one contribution
	AllDays int
	Contrib int // repositories contributed to
	Commits int
	PRs     int
	Issues  int
	Stars   int
	Forks   int
	Repos   int
	Langs   []lang
}

type lang struct {
	Name  string
	Share float64
}

func fetch(login, token string) (data, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]string{"login": login}})
	req, _ := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	req.Header.Set("Authorization", "bearer "+token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return data{}, err
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			User struct {
				Login string
				CC    struct {
					TotalCommitContributions                int
					RestrictedContributionsCount            int
					TotalPullRequestContributions           int
					TotalIssueContributions                 int
					TotalRepositoriesWithContributedCommits int
					ContributionCalendar                    struct {
						TotalContributions int
						Weeks              []struct {
							ContributionDays []struct{ ContributionCount int }
						}
					}
				} `json:"contributionsCollection"`
				Repositories struct {
					TotalCount int
					Nodes      []struct {
						StargazerCount int
						ForkCount      int
						Languages      struct {
							Edges []struct {
								Size int
								Node struct{ Name string }
							}
						}
					}
				}
			}
		}
		Errors []struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return data{}, err
	}
	if len(r.Errors) > 0 {
		return data{}, fmt.Errorf("graphql: %s", r.Errors[0].Message)
	}
	u := r.Data.User
	d := data{Login: u.Login, Commits: u.CC.TotalCommitContributions + u.CC.RestrictedContributionsCount,
		PRs: u.CC.TotalPullRequestContributions, Issues: u.CC.TotalIssueContributions, Repos: u.Repositories.TotalCount}
	d.Total = u.CC.ContributionCalendar.TotalContributions
	d.Contrib = u.CC.TotalRepositoriesWithContributedCommits
	for _, w := range u.CC.ContributionCalendar.Weeks {
		for _, day := range w.ContributionDays {
			d.AllDays++
			if day.ContributionCount > 0 {
				d.Days++
			}
		}
	}
	sizes := map[string]int{}
	total := 0
	for _, n := range u.Repositories.Nodes {
		d.Stars += n.StargazerCount
		d.Forks += n.ForkCount
		for _, e := range n.Languages.Edges {
			sizes[e.Node.Name] += e.Size
			total += e.Size
		}
	}
	for n, s := range sizes {
		d.Langs = append(d.Langs, lang{n, float64(s) / float64(max(total, 1))})
	}
	sort.Slice(d.Langs, func(i, j int) bool { return d.Langs[i].Share > d.Langs[j].Share })
	// Markup and glue languages say little about what someone builds.
	var keep []lang
	for _, l := range d.Langs {
		if l.Name != "HTML" && l.Name != "CSS" && l.Name != "Batchfile" && l.Name != "Dockerfile" && l.Name != "Makefile" {
			keep = append(keep, l)
		}
	}
	if len(keep) > 5 {
		keep = keep[:5]
	}
	sum := 0.0
	for _, l := range keep {
		sum += l.Share
	}
	for i := range keep {
		keep[i].Share /= sum
	}
	d.Langs = keep
	return d, nil
}

type theme struct {
	bg, border, ink, muted, faint, accent string
	langs                                 []string
}

var (
	dark  = theme{"#0f1320", "#232a3d", "#e7e9f4", "#8a8fad", "#2a2f45", "#a9d6ff", []string{"#a9d6ff", "#7ea4ff", "#b3a6ff", "#5b63d9", "#8a8fad"}}
	light = theme{"#f7f9fc", "#d6dce6", "#151a2c", "#5d6480", "#e3e7ef", "#3d6fd6", []string{"#3d6fd6", "#5b63d9", "#7a5fd0", "#2f8fd1", "#8a8fad"}}
)

// plural picks the Russian form for n: one (1, 21), few (2-4), many.
func plural(n int, one, few, many string) string {
	n %= 100
	switch {
	case n >= 11 && n <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	}
	return many
}

func num(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// icon draws a 14px line glyph at (x, y) (top-left).
func icon(name string, x, y int, c string) string {
	g := map[string]string{
		"star":   `<path d="M7 1.2l1.75 3.6 3.95.55-2.85 2.8.68 3.95L7 10.25 3.47 12.1l.68-3.95L1.3 5.35l3.95-.55z"/>`,
		"commit": `<circle cx="7" cy="7" r="2.6"/><path d="M0.5 7h3.9M9.6 7h3.9"/>`,
		"pr":     `<circle cx="3.2" cy="2.6" r="1.7"/><circle cx="3.2" cy="11.4" r="1.7"/><circle cx="10.8" cy="11.4" r="1.7"/><path d="M3.2 4.3v5.4M10.8 9.7V5.2a2 2 0 0 0-2-2H6.4"/>`,
		"issue":  `<circle cx="7" cy="7" r="5.6"/><circle cx="7" cy="7" r="0.9" fill="` + c + `"/>`,
		"repo":   `<path d="M2.4 12.2V2.6a1.4 1.4 0 0 1 1.4-1.4h7.8v9.2H3.8a1.4 1.4 0 0 0-1.4 1.4 1.4 1.4 0 0 0 1.4 1.4h7.8"/>`,
	}[name]
	return fmt.Sprintf(`<g transform="translate(%d %d)" fill="none" stroke="%s" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">%s</g>`, x, y, c, g)
}

const sans = `-apple-system, 'Segoe UI', 'Inter', 'Helvetica Neue', Arial, sans-serif`

func panelStart(b *strings.Builder, w, h int, t theme, label string) {
	fmt.Fprintf(b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s">`, w, h, w, h, html.EscapeString(label))
	fmt.Fprintf(b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="14" fill="%s" stroke="%s"/>`, w-1, h-1, t.bg, t.border)
}

func render(d data, t theme) string {
	var b strings.Builder
	w, h := 900, 250
	panelStart(&b, w, h, t, "Статистика GitHub "+d.Login)
	fmt.Fprintf(&b, `<g font-family="%s">`, sans)
	fmt.Fprintf(&b, `<text x="48" y="52" font-size="17" font-weight="600" fill="%s">Статистика GitHub</text>`, t.ink)
	fmt.Fprintf(&b, `<text x="852" y="52" font-size="13" fill="%s" text-anchor="end">%s · за год</text>`, t.muted, html.EscapeString(d.Login))
	fmt.Fprintf(&b, `<rect x="48" y="68" width="%d" height="1" fill="%s"/>`, w-96, t.faint)

	// column 1: counters
	rows := []struct {
		ic, label string
		v         int
	}{{"commit", "Коммиты", d.Commits}, {"star", "Звёзды", d.Stars}, {"pr", "Pull requests", d.PRs}, {"issue", "Issues", d.Issues}, {"repo", "Репозитории", d.Repos}}
	for i, r := range rows {
		y := 100 + i*28
		b.WriteString(icon(r.ic, 48, y-11, t.accent))
		fmt.Fprintf(&b, `<text x="72" y="%d" font-size="14" fill="%s">%s</text>`, y, t.muted, r.label)
		fmt.Fprintf(&b, `<text x="300" y="%d" font-size="14" font-weight="600" fill="%s" text-anchor="end">%s</text>`, y, t.ink, num(r.v))
	}

	// column 2: languages, one bar each
	fmt.Fprintf(&b, `<text x="360" y="96" font-size="11" fill="%s" letter-spacing="1.2">ЯЗЫКИ</text>`, t.muted)
	for i, l := range d.Langs {
		y := 118 + i*24
		col := t.langs[i%len(t.langs)]
		fmt.Fprintf(&b, `<text x="360" y="%d" font-size="13" fill="%s">%s</text>`, y, t.ink, html.EscapeString(l.Name))
		fmt.Fprintf(&b, `<text x="620" y="%d" font-size="12" fill="%s" text-anchor="end">%.0f%%</text>`, y, t.muted, l.Share*100)
		fmt.Fprintf(&b, `<rect x="470" y="%d" width="110" height="5" rx="2.5" fill="%s"/>`, y-8, t.faint)
		fmt.Fprintf(&b, `<rect x="470" y="%d" width="%.1f" height="5" rx="2.5" fill="%s"/>`, y-8, max(3, l.Share*110), col)
	}

	// column 3: ring — share of days with activity
	cx, cy, r := 760, 152, 50
	share := 0.0
	if d.AllDays > 0 {
		share = float64(d.Days) / float64(d.AllDays)
	}
	circ := 2 * 3.14159265 * float64(r)
	fmt.Fprintf(&b, `<defs><linearGradient id="ring" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient></defs>`, t.langs[0], t.langs[2])
	fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%d" fill="none" stroke="%s" stroke-width="8"/>`, cx, cy, r, t.faint)
	fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="%d" fill="none" stroke="url(#ring)" stroke-width="8" stroke-linecap="round" stroke-dasharray="%.1f %.1f" transform="rotate(-90 %d %d)">`, cx, cy, r, max(0.01, share)*circ, circ, cx, cy)
	fmt.Fprintf(&b, `<animate attributeName="stroke-dasharray" from="0 %.1f" to="%.1f %.1f" dur="1.4s" fill="freeze" calcMode="spline" keySplines="0.2 0.8 0.2 1" keyTimes="0;1"/></circle>`, circ, max(0.01, share)*circ, circ)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="24" font-weight="600" fill="%s" text-anchor="middle">%d</text>`, cx, cy+4, t.ink, d.Days)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" fill="%s" text-anchor="middle">%s</text>`, cx, cy+20, t.muted, plural(d.Days, "день", "дня", "дней"))
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" fill="%s" text-anchor="middle">активность за год</text>`, cx, cy+r+26, t.muted)
	b.WriteString(`</g></svg>`)
	return b.String()
}

// wrapSnake puts the generated contribution snake into the same panel as
// the other cards, with a caption.
func wrapSnake(snake []byte, d data, t theme) (string, error) {
	s := string(snake)
	i := strings.Index(s, "<svg")
	if i < 0 {
		return "", fmt.Errorf("not an svg")
	}
	s = s[i:]
	// place the snake's own <svg> inside ours
	end := strings.Index(s, ">")
	inner := `<svg x="10" y="62" width="880" height="192"` + strings.TrimPrefix(s[:end], "<svg") + ">" + s[end+1:]
	inner = strings.Replace(inner, `width="880" height="192" viewBox`, `viewBox`, 1)
	var b strings.Builder
	w, h := 900, 270
	panelStart(&b, w, h, t, "Вклад за год")
	fmt.Fprintf(&b, `<g font-family="%s">`, sans)
	fmt.Fprintf(&b, `<text x="48" y="52" font-size="17" font-weight="600" fill="%s">Вклад за год</text>`, t.ink)
	fmt.Fprintf(&b, `<text x="852" y="52" font-size="13" fill="%s" text-anchor="end">%s %s · %d %s</text>`, t.muted,
		num(d.Total), plural(d.Total, "вклад", "вклада", "вкладов"), d.Days, plural(d.Days, "активный день", "активных дня", "активных дней"))
	b.WriteString(`</g>`)
	b.WriteString(inner)
	b.WriteString(`</svg>`)
	return b.String(), nil
}

func main() {
	login := flag.String("user", "FrainTX", "GitHub login")
	out := flag.String("out", "dist", "output directory")
	flag.Parse()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "GITHUB_TOKEN is required")
		os.Exit(1)
	}
	d, err := fetch(*login, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for name, t := range map[string]theme{"stats-dark.svg": dark, "stats-light.svg": light} {
		if err := os.WriteFile(filepath.Join(*out, name), []byte(render(d, t)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	// Wrap snakes generated earlier in the same run (raw-snake-*.svg).
	for _, v := range []struct {
		in, out string
		t       theme
	}{{"raw-snake-dark.svg", "snake-dark.svg", dark}, {"raw-snake-light.svg", "snake-light.svg", light}} {
		raw, err := os.ReadFile(filepath.Join(*out, v.in))
		if err != nil {
			continue
		}
		svg, err := wrapSnake(raw, d, v.t)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = os.WriteFile(filepath.Join(*out, v.out), []byte(svg), 0o644)
		_ = os.Remove(filepath.Join(*out, v.in))
	}
	fmt.Printf("%s: %d commits, %d stars, %d repos, langs %v\n", d.Login, d.Commits, d.Stars, d.Repos, d.Langs)
}
