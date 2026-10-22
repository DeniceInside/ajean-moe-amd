package ajean

// chat_tools_loose.go — une édition dont le passage n'est pas retrouvé mot pour
// mot. Un petit modèle recopie le passage avec ses propres
// espaces (tabulations en espaces, espace final perdu, indentation décalée d'un
// niveau) : l'édition échouait, le modèle relisait tout le fichier et échouait
// souvent de la même façon. Une personne verrait quelles lignes étaient visées :
// quand elles sont uniques, l'édition se fait ; quand aucune ne correspond,
// l'erreur montre le passage le plus proche, à recopier.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// looseEditLines : les lignes de old, sans les lignes vides autour.
func looseEditLines(old string) []string {
	ls := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")
	for len(ls) > 0 && strings.TrimSpace(ls[0]) == "" {
		ls = ls[1:]
	}
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

func squashSpaces(l string) string { return strings.Join(strings.Fields(l), " ") }

func lineIndent(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

// looseEdit : s où les lignes entières qui correspondent à old (aux espaces
// près) sont remplacées par repl, réindenté comme le fichier. at : la ligne
// (1 = première). ok est faux sauf si UN SEUL endroit correspond.
func looseEdit(s, old, repl string) (out string, at int, ok bool) {
	want := looseEditLines(old)
	if len(want) == 0 || strings.TrimSpace(strings.Join(want, "")) == "" {
		return "", 0, false
	}
	crlf := strings.Contains(s, "\r\n")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	pos := -1
	for i := 0; i+len(want) <= len(lines); i++ {
		match := true
		for j, w := range want {
			if squashSpaces(lines[i+j]) != squashSpaces(w) {
				match = false
				break
			}
		}
		if match {
			if pos >= 0 {
				return "", 0, false // deux fois : on ne sait pas lequel était visé
			}
			pos = i
		}
	}
	if pos < 0 {
		return "", 0, false
	}
	// Le remplacement prend l'indentation du fichier là où le passage en avait
	// une autre : chaque indentation du passage correspond à celle du fichier à
	// cette ligne (quatre espaces pour une tabulation, huit pour deux).
	mapped := map[string]string{}
	for j, w := range want {
		if g := lineIndent(w); mapped[g] == "" {
			mapped[g] = lineIndent(lines[pos+j])
		}
	}
	var repLines []string
	for _, l := range strings.Split(strings.ReplaceAll(strings.Trim(repl, "\r\n"), "\r\n", "\n"), "\n") {
		g := lineIndent(l)
		best := ""
		for from := range mapped { // la plus longue indentation du passage par laquelle la ligne commence
			if strings.HasPrefix(g, from) && len(from) > len(best) {
				best = from
			}
		}
		if to, ok := mapped[best]; ok && to != best {
			l = to + l[len(best):]
		}
		repLines = append(repLines, l)
	}
	if strings.TrimSpace(repl) == "" {
		repLines = nil // suppression du passage
	}
	lines = append(lines[:pos], append(repLines, lines[pos+len(want):]...)...)
	out = strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, pos + 1, true
}

// closestBudget : le nombre maximal de comparaisons de lignes de closestPassage.
const closestBudget = 50_000

// closestPassage : le passage de s qui ressemble le plus à old, avec ses
// numéros de ligne, pour que le modèle le recopie exactement ("" si rien
// d'assez proche).
func closestPassage(s, old string) string {
	want := looseEditLines(old)
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(want) == 0 || len(lines) == 0 {
		return ""
	}
	n := min(len(want), len(lines))
	best, bestAt := 0.0, -1
	wg := make([][]uint64, n)
	for j := range n {
		wg[j] = lineBigrams(squashSpaces(want[j]))
	}
	starts := make([]int, 0, len(lines)-n+1)
	for i := 0; i+n <= len(lines); i++ {
		starts = append(starts, i)
	}
	// Toutes les positions d'un long passage dans un long fichier, c'était trop
	// (300 lignes dans 20 000 : 82 s mesurées). Au-delà du budget, seules
	// les positions dont la première ou la dernière ligne ressemble le plus à
	// celles du passage sont évaluées en entier.
	if len(starts)*n > closestBudget {
		anchor := make([]float64, len(starts))
		for k, l := range lines {
			b := lineBigrams(squashSpaces(l))
			if k < len(starts) {
				anchor[k] = bigramDice(wg[0], b)
			}
			if i := k - n + 1; i >= 0 {
				anchor[i] = max(anchor[i], bigramDice(wg[n-1], b))
			}
		}
		sort.SliceStable(starts, func(a, b int) bool { return anchor[starts[a]] > anchor[starts[b]] })
		starts = starts[:max(1, closestBudget/n)]
		sort.Ints(starts)
	}
	lg := map[int][]uint64{} // les bigrammes de chaque ligne, calculés une fois
	line := func(i int) []uint64 {
		b, ok := lg[i]
		if !ok {
			b = lineBigrams(squashSpaces(lines[i]))
			lg[i] = b
		}
		return b
	}
	for _, i := range starts {
		score := 0.0
		for j := range n {
			score += bigramDice(wg[j], line(i+j))
		}
		if score /= float64(n); score > best {
			best, bestAt = score, i
		}
	}
	if bestAt < 0 || best < 0.5 {
		return ""
	}
	from, to := max(0, bestAt-1), min(len(lines), bestAt+n+1)
	if to-from > 16 {
		to = from + 16
	}
	var b strings.Builder
	for i := from; i < to; i++ {
		l := lines[i]
		if r := []rune(l); len(r) > 300 {
			l = string(r[:300]) + "…"
		}
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, l)
	}
	return b.String()
}

// lineBigrams : les paires de caractères de s en minuscules, triées (une paire
// présente deux fois l'est deux fois).
func lineBigrams(s string) []uint64 {
	r := []rune(strings.ToLower(s))
	if len(r) < 2 {
		return nil
	}
	b := make([]uint64, len(r)-1)
	for i := range b {
		b[i] = uint64(uint32(r[i]))<<32 | uint64(uint32(r[i+1]))
	}
	slices.Sort(b)
	return b
}

// bigramDice : la ressemblance de deux listes de bigrammes, de 0 à 1.
func bigramDice(a, b []uint64) float64 {
	if len(a)+len(b) == 0 {
		return 1
	}
	common := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			common++
			i++
			j++
		}
	}
	return 2 * float64(common) / float64(len(a)+len(b))
}
