package ajean

import (
	"os"
	"strings"
	"testing"
)

// Les codes de terminal et les barres de progression redessinées disparaissent ;
// le texte, les accents et les sauts de ligne restent.
func TestCleanTermOutput(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[32mok\x1b[0m terminé":             "ok terminé",
		"10%\r50%\r100%\nfini":                  "100%\nfini",
		"\x1b]0;titre\x07texte":                 "texte",
		"a\bb":                                  "b",
		"ligne\r\nsuivante\x1b(B":               "ligne\nsuivante",
		"café\x1b\nnormal":                      "café\nnormal",
		"rien à nettoyer\n\tindenté":            "rien à nettoyer\n\tindenté",
		"npm \x1b[1G\x1b[0K⠙\x1b[1G\x1b[0Kfait": "npm ⠙fait",
	} {
		if got := cleanTermOutput(in); got != want {
			t.Errorf("%q : %q, attendu %q", in, got, want)
		}
	}
}

// Une sortie trop longue garde son début et sa fin, et la sortie entière est
// enregistrée dans un fichier cité dans la coupe.
func TestCmdOutputKeepsHeadAndTail(t *testing.T) {
	long := "DEBUT" + strings.Repeat("x", 20000) + "FIN"
	out := cmdOutput(long)
	if !strings.HasPrefix(out, "DEBUT") || !strings.HasSuffix(out, "FIN") || !strings.Contains(out, "caractères coupés") {
		t.Fatalf("début ou fin perdu : %q…", out[:80])
	}
	i := strings.Index(out, "la sortie entière est dans ")
	if i < 0 {
		t.Fatal("pas de fichier cité")
	}
	path := strings.TrimSpace(strings.SplitN(out[i+len("la sortie entière est dans "):], " :", 2)[0])
	b, err := os.ReadFile(path)
	if err != nil || string(b) != long {
		t.Fatalf("fichier %s : %v", path, err)
	}
	os.Remove(path)
	if short := cmdOutput("court"); short != "court" {
		t.Errorf("une sortie courte reste entière : %q", short)
	}
}
