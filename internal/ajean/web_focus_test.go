package ajean

import (
	"strings"
	"testing"
)

// Une longue page : seuls les passages sur le focus reviennent, avec leurs
// numéros de ligne ; une page courte revient entière ; rien de commun : "".
func TestFocusedPage(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "## Actualité "+strings.Repeat("x", 3), "La mairie annonce une fête de quartier, des travaux et un marché de producteurs locaux.", "")
	}
	lines = append(lines, "## Piscine municipale", "Horaires d'ouverture : du lundi au vendredi de 12 h à 19 h, le samedi de 9 h à 18 h.", "")
	for i := 0; i < 30; i++ {
		lines = append(lines, "Encore une actualité sans rapport avec le sujet recherché ici.", "")
	}
	e := &cacheEntry{url: "https://ville.example/piscine", lines: lines}
	out := focusedPage(e, "horaires de la piscine")
	if !strings.Contains(out, "Horaires d'ouverture") || strings.Count(out, "Actualité") > 3 || !strings.Contains(out, "## lignes 181 à") {
		t.Fatalf("passages ciblés :\n%s", out)
	}
	if focusedPage(e, "zzz introuvable qqq") != "" {
		t.Error("rien de commun : vide (le plan est rendu à la place)")
	}
	short := &cacheEntry{url: "u", lines: []string{"courte", "page"}}
	if got := focusedPage(short, "page"); !strings.Contains(got, "rendue entière") || !strings.Contains(got, "2 | page") {
		t.Errorf("page courte : %q", got)
	}
}
