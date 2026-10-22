package ajean

// jean_exact.go — les mots d'une recherche que seule une correspondance exacte
// retrouve : codes d'erreur, noms de fichiers, adresses, versions (0x80070005,
// app.py, ECONNREFUSED, 192.168.1.10). Par le sens, « 0x80070005 » et
// « 0x80004005 » se ressemblent : la ligne du journal qui portait le bon code
// passait sous le seuil et devenait introuvable. On fusionne donc les
// classements (sens, mots) avec un bonus par jeton exact présent.

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	jeanRRFK       = 60
	jeanExactBonus = 1.0 / jeanRRFK // un jeton exact vaut une première place
)

var (
	// jeanMeasureRe : un nombre et son unité ou son rang (20km, 10h30, 2ème) :
	// écrit pareil par beaucoup d'entrées, il n'en distingue aucune.
	jeanMeasureRe = regexp.MustCompile(`^\d+(?:[.,]\d+)?\p{L}{1,4}\d{0,2}$`)
	jeanHexRe     = regexp.MustCompile(`^0[xX][0-9a-fA-F]{2,}$`)
)

// jeanExactTokens : les jetons exacts de q, repliés (minuscules, sans accents).
func jeanExactTokens(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'`«»“”‘’()[]{}<>,;!?", r)
	}) {
		w = strings.TrimRight(w, ".:") // la fin d'une phrase
		if f := foldSearch(w); jeanExactToken(w) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func jeanExactToken(w string) bool {
	if len([]rune(w)) < 4 {
		return false
	}
	if jeanHexRe.MatchString(w) {
		return true
	}
	if jeanMeasureRe.MatchString(w) {
		return false
	}
	letters, upper, digits, marks := 0, 0, 0, 0
	for _, r := range w {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsLetter(r):
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		case strings.ContainsRune("._/\\", r):
			marks++
		}
	}
	switch {
	case letters > 0 && (digits > 0 || marks > 0): // app.py, go1.25, src/main.go, max_tokens
		return true
	case letters == 0 && digits > 0 && strings.Count(w, ".") >= 2: // 192.168.1.10, 1.25.3
		return true
	}
	return letters >= 5 && upper == letters // ECONNREFUSED, ENOENT
}

// jeanExactHits : combien des jetons exacts figurent dans le texte (replié).
func jeanExactHits(text string, exact []string) int {
	if len(exact) == 0 {
		return 0
	}
	f, n := foldSearch(text), 0
	for _, t := range exact {
		if strings.Contains(f, t) {
			n++
		}
	}
	return n
}

// jeanFuse : les entrées que keep accepte, classées par fusion des rangs
// (reciprocal rank fusion) plus un bonus par jeton exact présent, les
// meilleures d'abord ; à égalité, la plus récente.
func jeanFuse(all []jeanEntry, rankings [][]int, exact []string, keep func(i, exactHits int) bool) []int {
	score := map[int]float64{}
	for _, r := range rankings {
		for k, i := range r {
			score[i] += 1 / float64(jeanRRFK+k+1)
		}
	}
	hits := map[int]int{}
	if len(exact) > 0 {
		for i, e := range all {
			if n := jeanExactHits(e.Text, exact); n > 0 {
				hits[i] = n
				score[i] += jeanExactBonus * float64(n)
			}
		}
	}
	var out []int
	for i := range score {
		if keep(i, hits[i]) {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if sa, sb := score[out[a]], score[out[b]]; sa != sb {
			return sa > sb
		}
		return out[a] > out[b]
	})
	return out
}
