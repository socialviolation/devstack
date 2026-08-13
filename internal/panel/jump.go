package panel

import (
	"sort"
	"strings"
)

type link struct {
	Label string
	Group string
	URL   string
	State string
}

func (l link) searchText() string {
	return strings.ToLower(l.Label + " " + l.Group + " " + l.URL)
}

func collectLinks(snap Snapshot) []link {
	var out []link
	add := func(group string, services []Service) {
		for _, svc := range services {
			for _, url := range svc.URLs {
				out = append(out, link{Label: svc.Name, Group: group, URL: url, State: svc.State})
			}
		}
	}
	add("machine", snap.Infra)
	for _, ws := range snap.Workspaces {
		add("containers", ws.Infra)
		add("base", ws.Base)
		for _, st := range ws.Stacks {
			add(st.Name, st.Services)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].State == "running", out[j].State == "running"; a != b {
			return a
		}
		return tailnet(out[i].URL) && !tailnet(out[j].URL)
	})
	return out
}

func tailnet(url string) bool {
	return !strings.Contains(url, "localhost") && !strings.Contains(url, "127.0.0.1")
}

func filterLinks(links []link, query string) []link {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return links
	}

	type scored struct {
		link  link
		score int
		order int
	}
	var hits []scored
	for i, l := range links {
		if score, ok := fuzzyScore(l.searchText(), query); ok {
			hits = append(hits, scored{link: l, score: score, order: i})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].order < hits[j].order
	})

	out := make([]link, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.link)
	}
	return out
}

func fuzzyScore(text, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	runes := []rune(text)
	score := 0
	at := 0
	previous := -2

	for _, want := range query {
		if want == ' ' {
			continue
		}
		found := -1
		for i := at; i < len(runes); i++ {
			if runes[i] == want {
				found = i
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		switch {
		case found == previous+1:
			score += 8
		case found == 0 || isBoundary(runes[found-1]):
			score += 6
		default:
			score += 1
		}
		if found < 12 {
			score++
		}
		previous = found
		at = found + 1
	}
	return score, true
}

func isBoundary(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', ':', '@':
		return true
	}
	return false
}
