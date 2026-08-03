package stack

import (
	"fmt"
	"sort"
	"strings"
)

// Coverage is how much of a group a stack actually runs. A stack overlays the
// services it changes plus their callers, so naming a group at create time does
// not guarantee the whole group came with it, and the rest keep serving from
// base.
type Coverage struct {
	Group   string
	In      []string
	Missing []string
}

func (c Coverage) Complete() bool { return len(c.Missing) == 0 }

// Label reads "core 3/3" or "core 1/3", the short form for a table cell.
func (c Coverage) Label() string {
	return fmt.Sprintf("%s %d/%d", c.Group, len(c.In), len(c.In)+len(c.Missing))
}

// Sentence names what is missing and where it runs instead, because "1/3" tells
// a reader the count and not the consequence.
func (c Coverage) Sentence() string {
	if c.Complete() {
		return fmt.Sprintf("covers group %s (%d/%d)", c.Group, len(c.In), len(c.In))
	}
	return fmt.Sprintf("covers group %s (%d/%d — %s serve%s from base)",
		c.Group, len(c.In), len(c.In)+len(c.Missing),
		strings.Join(c.Missing, ", "), plural(len(c.Missing)))
}

func plural(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}

// CoverageOf reports, for each group in groups, which of its members this
// overlay runs and which it does not. baseGroups is the base workspace's group
// map: a stack's own manifest lists only the members that made it into the
// overlay, so asking it what a group contains can never reveal a shortfall.
func CoverageOf(groups []string, overlay []string, baseGroups map[string][]string) []Coverage {
	if len(groups) == 0 {
		return nil
	}
	inOverlay := stringSet(overlay)
	out := make([]Coverage, 0, len(groups))
	for _, g := range groups {
		members, ok := baseGroups[g]
		if !ok {
			continue
		}
		cov := Coverage{Group: g}
		for _, m := range members {
			if inOverlay[m] {
				cov.In = append(cov.In, m)
				continue
			}
			cov.Missing = append(cov.Missing, m)
		}
		sort.Strings(cov.In)
		sort.Strings(cov.Missing)
		out = append(out, cov)
	}
	return out
}
