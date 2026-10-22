package ajean

import (
	"strings"
	"testing"
)

// Un passage recopié avec d'autres espaces est retrouvé s'il est unique, et le
// remplacement prend l'indentation du fichier.
func TestLooseEdit(t *testing.T) {
	file := "func f() {\n\tif ok {\n\t\treturn 1\n\t}\n}\n"
	out, at, ok := looseEdit(file, "    if ok {\n        return 1\n    }", "    if ok {\n        return 2\n    }")
	if !ok || at != 2 || out != "func f() {\n\tif ok {\n\t\treturn 2\n\t}\n}\n" {
		t.Fatalf("ok=%v at=%d\n%q", ok, at, out)
	}
	// Deux endroits possibles : rien n'est modifié.
	if _, _, ok := looseEdit("a  b\nx\na b\n", "a b", "c"); ok {
		t.Error("passage ambigu : refusé")
	}
	// Suppression du passage.
	if out, _, ok := looseEdit("un\n  deux \ntrois\n", "deux", ""); !ok || out != "un\ntrois\n" {
		t.Errorf("suppression : %q %v", out, ok)
	}
}

// Rien d'exact ni d'approché aux espaces près : l'erreur montre le plus proche.
func TestClosestPassage(t *testing.T) {
	file := "ligne un\nconst limite = 8000 // sortie\nligne trois\n"
	near := closestPassage(file, "const limite = 9000 // sortie")
	if !strings.Contains(near, "     2\tconst limite = 8000") {
		t.Fatalf("passage proche : %q", near)
	}
	if closestPassage(file, "rien à voir du tout ici zzzz") != "" {
		t.Error("rien de proche : vide")
	}
}
