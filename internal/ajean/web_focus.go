package ajean

// web_focus.go — web_open avec un focus : une longue page rendue par ses
// passages qui parlent de ce que le modèle cherche.
//
// Les horaires d'une piscine municipale étaient en bas de sa page, sous les
// actualités : lue par tranches, un petit modèle feuilletait menus et articles,
// ou s'arrêtait à la première tranche en disant que les horaires n'y étaient
// pas. Une personne fait défiler jusqu'à la partie qui répond. Les passages
// gardent leurs numéros de ligne : web_read lit la suite autour.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	focusChars  = 6000 // ce que remplissent les passages choisis
	focusTop    = 10   // les meilleurs passages regardés, au plus
	passageMin  = 600
	passageMax  = 900
	focusSmall  = focusChars // une page plus courte est rendue entière
	headingMark = "#"
)

// pagePassage : des lignes consécutives de la page (from, to : 1 = première).
type pagePassage struct {
	from, to int
	text     string
}

// pageBlocks : les paragraphes de la page (entre lignes vides) ; un bloc de code
// délimité n'est jamais coupé à ses lignes vides.
func pageBlocks(lines []string) []pagePassage {
	var out []pagePassage
	start, fence := -1, false
	flush := func(end int) {
		if start >= 0 {
			out = append(out, pagePassage{start + 1, end, strings.Join(lines[start:end], "\n")})
			start = -1
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		}
		if strings.TrimSpace(l) == "" && !fence {
			flush(i)
		} else if start < 0 {
			start = i
		}
	}
	flush(len(lines))
	return out
}

// pagePassages regroupe les paragraphes en passages de 600 à 900 caractères,
// coupés avant un titre ; un paragraphe trop long est découpé par lignes.
func pagePassages(lines []string) []pagePassage {
	var out []pagePassage
	var cur *pagePassage
	curLen := 0
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
		}
		cur, curLen = nil, 0
	}
	for _, b := range pageBlocks(lines) {
		n := utf8.RuneCountInString(b.text)
		if curLen > 0 && (curLen+n > passageMax || curLen >= passageMin || strings.HasPrefix(b.text, headingMark) && curLen >= passageMin/2) {
			flush()
		}
		if n > passageMax { // un long paragraphe ou bloc de code : en morceaux de lignes
			flush()
			from, size := b.from, 0
			for i := b.from; i <= b.to; i++ {
				size += utf8.RuneCountInString(lines[i-1]) + 1
				if size >= passageMax || i == b.to {
					out = append(out, pagePassage{from, i, strings.Join(lines[from-1:i], "\n")})
					from, size = i+1, 0
				}
			}
			continue
		}
		if cur == nil {
			cur = &pagePassage{from: b.from, to: b.to, text: b.text}
		} else {
			cur.to = b.to
			cur.text += "\n\n" + b.text
		}
		curLen += n + 2
	}
	flush()
	return out
}

// passagesByKeywords : les passages qui contiennent des mots du focus, les
// meilleurs d'abord. Un mot présent dans peu de passages pèse plus qu'un mot
// présent partout (le nom du site est sur toutes les lignes de sa page).
func passagesByKeywords(focus string, ps []pagePassage) []int {
	ts := webTerms(focus)
	if len(ts) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, t := range ts {
		want[t] = true
	}
	tf := make([]map[string]int, len(ps))
	df := map[string]int{}
	for i, p := range ps {
		tf[i] = map[string]int{}
		for _, w := range webWords(p.text) {
			if want[w] {
				if tf[i][w] == 0 {
					df[w]++
				}
				tf[i][w]++
			}
		}
	}
	score := make([]float64, len(ps))
	var order []int
	for i := range ps {
		for w, n := range tf[i] {
			score[i] += math.Log(1+float64(len(ps))/float64(df[w])) * (1 + math.Log(float64(n)))
		}
		if score[i] > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return score[order[a]] > score[order[b]] })
	return order
}

// focusedPage : les passages de la page sur focus, dans l'ordre de la page,
// numérotés comme web_read, puis comment lire autour. "" quand rien ne
// correspond (l'appelant rend alors le plan habituel).
func focusedPage(e *cacheEntry, focus string) string {
	chars := 0
	for _, l := range e.lines {
		chars += utf8.RuneCountInString(l) + 1
	}
	if chars <= focusSmall {
		return fmt.Sprintf("# Ouvert : %s (%d lignes, page courte rendue entière)\n\n%s", e.url, len(e.lines), formatLines(e.lines, 1))
	}
	ps := pagePassages(e.lines)
	order := passagesByKeywords(focus, ps)
	if len(order) == 0 {
		return ""
	}
	var chosen []int
	used := 0
	for _, i := range order[:min(len(order), focusTop)] {
		n := utf8.RuneCountInString(ps[i].text)
		if used > 0 && used+n > focusChars {
			continue
		}
		chosen = append(chosen, i)
		if used += n; used >= focusChars-passageMin/2 {
			break
		}
	}
	sort.Ints(chosen)
	var b strings.Builder
	fmt.Fprintf(&b, "# Ouvert : %s (%d lignes, %d caractères) : passages sur « %s »\n\n", e.url, len(e.lines), chars, focus)
	for _, i := range chosen {
		p := ps[i]
		fmt.Fprintf(&b, "## lignes %d à %d\n%s\n\n", p.from, p.to, formatLines(e.lines[p.from-1:p.to], p.from))
	}
	what := fmt.Sprintf("%d passages choisis", len(chosen))
	if len(chosen) == 1 {
		what = "1 passage choisi"
	}
	fmt.Fprintf(&b, "(%s par leurs mots ; pour lire autour d'un passage : web_read avec son numéro de ligne en offset)", what)
	return b.String()
}
