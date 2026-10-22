package ajean

import (
	"strings"
	"testing"
	"time"
)

func TestJeanConsolidateBrief(t *testing.T) {
	now := time.Now()
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }
	all := []jeanEntry{
		{When: day(1), Kind: "echange", Text: "Utilisateur : exporte les factures du mois en PDF\nJean : fait"},
		{When: day(8), Kind: "echange", Text: "Utilisateur : peux-tu exporter les factures de septembre en PDF ?\nJean : fait"},
		{When: day(15), Kind: "echange", Text: "Utilisateur : export des factures en PDF stp\nJean : fait"},
		{When: day(2), Kind: "echange", Text: "Utilisateur : merci\nJean : avec plaisir"},
		{When: day(3), Kind: "echange", Text: "Utilisateur : sauvegarde les photos sur le NAS\nJean : fait"},
		{When: day(4), Kind: "echange", Text: "Utilisateur : sauvegarde des photos sur le NAS\nJean : fait"},
		{When: day(5), Kind: "echange", Text: "Utilisateur : refais la sauvegarde des photos NAS\nJean : fait"},
		{When: day(200), Kind: "echange", Text: "Utilisateur : exporte les factures en PDF\nJean : fait"}, // trop vieux
		{When: day(2), Kind: jeanFixKind, Text: "Erreur : bash: python: command not found\nVue : x\nCorrectif : `python a.py` a échoué ; puis `python3 a.py` a marché"},
		{When: day(9), Kind: jeanFixKind, Text: "Erreur : bash: python: command not found\nVue : x\nCorrectif : utiliser python3"},
		{When: day(9), Kind: jeanFixKind, Text: "Erreur : permission denied\nVue : x\nCorrectif : sudo"},
	}
	errs := jeanRecurringErrors(all, now)
	if !strings.Contains(errs, "python: command not found (2 times)") || !strings.Contains(errs, "python3 a.py") || strings.Contains(errs, "permission denied") {
		t.Errorf("erreurs récurrentes :\n%s", errs)
	}
}
