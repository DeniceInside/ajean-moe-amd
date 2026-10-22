package ajean

// jean_consolidate_brief.go — ce que la consolidation regarde EN PLUS de la
// mémoire elle-même : les erreurs qui reviennent (ses correctifs). Ranger ne
// suffit pas : une erreur rencontrée encore et encore doit être dite par une
// leçon ou une fiche.
//
// Les demandes refaites sans fiche ont été essayées puis retirées : sans le
// nombre d'actions de chaque échange (que le journal ne garde pas), le tri par
// mots ne remontait sur un vrai journal que du bavardage. Court par construction
// (8 lignes, 90 jours) : chaque ligne de bruit coûte à la consolidation.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	jeanBriefDays  = 90
	jeanBriefLines = 8
)

// jeanRecurringErrors : les erreurs rencontrées plusieurs fois (journal des
// correctifs), avec le dernier correctif. jeanMu tenu.
func jeanRecurringErrors(all []jeanEntry, now time.Time) string {
	since := now.AddDate(0, 0, -jeanBriefDays)
	type agg struct {
		n   int
		fix string
		at  time.Time
	}
	by := map[string]*agg{}
	for _, e := range all {
		if e.Kind != jeanFixKind || e.When.Before(since) {
			continue
		}
		f, ok := parseJeanFix(e.Text)
		if !ok {
			continue
		}
		a := by[f.sig]
		if a == nil {
			a = &agg{}
			by[f.sig] = a
		}
		a.n++
		if e.When.After(a.at) {
			a.fix, a.at = f.fix, e.When
		}
	}
	var sigs []string
	for s, a := range by {
		if a.n >= 2 {
			sigs = append(sigs, s)
		}
	}
	sort.Slice(sigs, func(i, j int) bool { return by[sigs[i]].n > by[sigs[j]].n })
	var b strings.Builder
	for _, s := range sigs[:min(len(sigs), jeanBriefLines)] {
		fmt.Fprintf(&b, "- %s (%d times) · last fix: %s\n", s, by[s].n, clipRunes(squashSpaces(by[s].fix), 200))
	}
	return b.String()
}

// jeanBriefSections : ce qui est ajouté au dump de la consolidation ("" quand il
// n'y a rien à signaler). jeanMu tenu.
func jeanBriefSections() string {
	all := jeanJournalAll()
	now := time.Now()
	var b strings.Builder
	if s := jeanRecurringErrors(all, now); s != "" {
		b.WriteString("\n## ERRORS MET AGAIN AND AGAIN (last 90 days)\n" + s)
	}
	return b.String()
}
