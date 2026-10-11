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
					TotalCommitContributions      int
					RestrictedContributionsCount  int
					TotalPullRequestContributions int
					TotalIssueContributions       int
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

func num(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func render(d data, t theme) string {
	var b strings.Builder
	w, h := 900, 200
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="Статистика GitHub %s">`, w, h, w, h, html.EscapeString(d.Login))
	fmt.Fprintf(&b, `<rect x="0.5" y="0.5" width="%d" height="%d" rx="14" fill="%s" stroke="%s"/>`, w-1, h-1, t.bg, t.border)
	b.WriteString(`<g font-family="-apple-system, 'Segoe UI', 'Inter', 'Helvetica Neue', Arial, sans-serif">`)
	// numbers
	stats := []struct {
		v     int
		label string
	}{{d.Commits, "коммитов за год"}, {d.Stars, "звёзд"}, {d.PRs, "pull requests"}, {d.Issues, "issues"}, {d.Repos, "репозиториев"}}
	shown := stats[:0:0]
	for i, s := range stats {
		if s.v > 0 || i == 0 {
			shown = append(shown, s) // empty counters only make a card look bare
		}
	}
	for i, s := range shown {
		x := 48 + i*118
		fmt.Fprintf(&b, `<text x="%d" y="78" font-size="28" font-weight="600" fill="%s">%s</text>`, x, t.ink, num(s.v))
		fmt.Fprintf(&b, `<text x="%d" y="100" font-size="12" fill="%s">%s</text>`, x, t.muted, s.label)
	}
	// languages
	lx, ly, lw := 540, 60, 312
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" fill="%s" letter-spacing="1">ЯЗЫКИ</text>`, lx, ly-14, t.muted)
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="8" rx="4" fill="%s"/>`, lx, ly, lw, t.faint)
	fmt.Fprintf(&b, `<clipPath id="bar"><rect x="%d" y="%d" width="%d" height="8" rx="4"/></clipPath><g clip-path="url(#bar)">`, lx, ly, lw)
	x := float64(lx)
	for i, l := range d.Langs {
		seg := l.Share * float64(lw)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="%.1f" height="8" fill="%s"/>`, x, ly, seg+0.5, t.langs[i%len(t.langs)])
		x += seg
	}
	b.WriteString(`</g>`)
	for i, l := range d.Langs {
		cx := lx + (i%2)*160
		cy := ly + 32 + (i/2)*24
		fmt.Fprintf(&b, `<circle cx="%d" cy="%d" r="4" fill="%s"/>`, cx+4, cy-4, t.langs[i%len(t.langs)])
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="13" fill="%s">%s <tspan fill="%s">%.0f%%</tspan></text>`, cx+16, cy, t.ink, html.EscapeString(l.Name), t.muted, l.Share*100)
	}
	// footer rule
	fmt.Fprintf(&b, `<rect x="48" y="150" width="%d" height="1" fill="%s"/>`, w-96, t.faint)
	fmt.Fprintf(&b, `<text x="48" y="176" font-size="12" fill="%s">публичные репозитории · обновлено %s</text>`, t.muted, time.Now().UTC().Format("02.01.2006"))
	b.WriteString(`</g></svg>`)
	return b.String()
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
	fmt.Printf("%s: %d commits, %d stars, %d repos, langs %v\n", d.Login, d.Commits, d.Stars, d.Repos, d.Langs)
}
