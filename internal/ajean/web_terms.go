package ajean

// web_terms.go — les mots d'une requête ou d'une page, comparés comme une
// personne les lit : sans accents ni majuscules, et « horaires » rejoint
// « horaire ». Sert à la recherche (résultats qui ignorent
// la requête) et à la lecture ciblée d'une longue page (web_focus.go).

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// webFolder : minuscules sans accents, pour que « Béziers » soit « beziers ».
var webFolder = strings.NewReplacer("à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "æ", "ae", "ç", "c",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i", "î", "i", "ï", "i", "ñ", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "œ", "oe", "ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y", "ß", "ss", "’", "'")

func webFold(s string) string { return webFolder.Replace(strings.ToLower(s)) }

// webStopWords : les mots d'une question qui ne disent rien de la réponse.
var webStopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for with from that this what which how when where who why are was were can does did not
		les des une pour par sur dans avec sans est sont que qui quoi quel quelle quels quelles comment combien quand aux ces ses leur`) {
		m[w] = true
	}
	return m
}()

func webSplitWords(s string) []string {
	return strings.FieldsFunc(webFold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// webWords : les mots de s, réduits à leurs 6 premières lettres pour que
// « horaires » rencontre « horaire » et « piscines » « piscine ».
func webWords(s string) []string {
	ws := webSplitWords(s)
	for i, w := range ws {
		if utf8.RuneCountInString(w) > 6 {
			ws[i] = string([]rune(w)[:6])
		}
	}
	return ws
}

// webTerms : les mots de s qui valent d'être cherchés (sans mots vides, sans
// mots de moins de 3 lettres sauf s'ils portent un chiffre), sans doublon.
func webTerms(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range webSplitWords(s) {
		digit := strings.IndexFunc(w, unicode.IsDigit) >= 0
		if webStopWords[w] || utf8.RuneCountInString(w) < 3 && !digit {
			continue
		}
		if utf8.RuneCountInString(w) > 6 {
			w = string([]rune(w)[:6])
		}
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}
