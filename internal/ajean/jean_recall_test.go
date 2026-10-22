package ajean

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestJeanExactTokens(t *testing.T) {
	got := strings.Join(jeanExactTokens("Pourquoi app.py renvoie 0x80070005 et ECONNREFUSED sur 192.168.1.10 après 20km ?"), " ")
	if got != "app.py 0x80070005 econnrefused 192.168.1.10" {
		t.Errorf("jetons exacts : %q", got)
	}
}

// La même erreur dans un autre fichier, à une autre ligne, a la même signature ;
// deux codes différents non.
func TestErrorSignature(t *testing.T) {
	a := errorSignature("exit: 1\nstderr:\nError: cannot open /home/x/a.txt: permission denied (errno 13)")
	b := errorSignature("exit: 1\nstderr:\nError: cannot open /tmp/y/b.txt: permission denied (errno 13)")
	if a == "" || a != b {
		t.Fatalf("signatures %q / %q", a, b)
	}
	if errorSignature("HTTP 404 not found") == errorSignature("HTTP 401 not found") {
		t.Error("deux codes HTTP différents : deux signatures")
	}
}

// Une commande ratée puis une variante qui marche : le correctif est gardé, et
// la même erreur rencontrée plus tard le rappelle en une ligne.
func TestJeanFixesLoop(t *testing.T) {
	testHome(t)
	_ = os.MkdirAll(jeanDir(), 0o700)
	failed := map[string]jeanFailure{}
	bad := "exit: 127\n\nstderr:\nbash: python: command not found"
	if out := jeanNoteBashResult(failed, "python script.py", bad); out != bad {
		t.Fatalf("aucun correctif connu : le résultat reste tel quel, obtenu %q", out)
	}
	jeanNoteBashResult(failed, "python3 script.py", "exit: 0\n\nstdout:\nok")
	out := jeanNoteBashResult(map[string]jeanFailure{}, "python autre.py", "exit: 127\n\nstderr:\nbash: python: command not found")
	if !strings.Contains(out, "[mémoire] Erreur déjà rencontrée") || !strings.Contains(out, "python3 script.py") {
		t.Fatalf("le correctif doit revenir : %q", out)
	}
	// La même commande qui marche la seconde fois n'apprend rien.
	f2 := map[string]jeanFailure{}
	jeanNoteBashResult(f2, "go test ./...", "exit: 1\n\nstdout:\nFAIL something unexpected happened")
	jeanNoteBashResult(f2, "go test ./...", "exit: 0")
	if jeanKnownFix("exit: 1\n\nstdout:\nFAIL something unexpected happened") != "" {
		t.Error("la même commande relancée n'est pas un correctif")
	}
}

// Rappel par mots (moteur d'embeddings coupé) : la fiche dont le BUT correspond
// est donnée en entier, un échange passé proche revient ; un « merci » ne
// rappelle rien ; le bloc ne part jamais au journal.
func TestJeanRecallByWords(t *testing.T) {
	testHome(t)
	_ = os.MkdirAll(jeanDir(), 0o700)
	if _, err := JeanSave("sauvegarde-nas", "sauvegarder les photos sur le NAS", "1. monter le partage\n2. rsync -a photos/ /mnt/nas/photos"); err != nil {
		t.Fatal(err)
	}
	jeanLogExchange("comment je sauvegarde mes photos sur le NAS déjà ?", "avec rsync vers /mnt/nas/photos")
	since := time.Now().Add(time.Minute) // tout le journal est du passé
	block := jeanRecallFor("[lundi 2026-10-20 10:00 (Paris)] tu peux sauvegarder mes photos sur le NAS ?", since)
	if !strings.Contains(block, "Fiche sauvegarde-nas") || !strings.Contains(block, "rsync -a photos/") || !strings.Contains(block, "From your journal") {
		t.Fatalf("rappel :\n%s", block)
	}
	if got := jeanTakeRecalled(); len(got) != 1 || got[0] != "sauvegarde-nas" {
		t.Errorf("fiche suivie : %v", got)
	}
	if jeanRecallFor("merci beaucoup", since) != "" {
		t.Error("un merci ne rappelle rien")
	}
	if jeanRecallFor("quelle heure est-il à Tokyo maintenant ?", since) != "" {
		t.Error("rien de commun : pas de bloc (le contexte reste propre)")
	}
	msg := "[lundi 2026-10-20 10:00 (Paris)] sauvegarde mes photos" + block
	if got := jeanCleanUserText(msg); got != "sauvegarde mes photos" {
		t.Errorf("le texte journalisé garde le rappel : %q", got)
	}
}

func TestJeanCarryNote(t *testing.T) {
	old := []Message{
		{Role: "user", Content: "[lundi 2026-10-20 22:00 (Paris)] rappelle-moi le code du portail"},
		{Role: "assistant", Content: "C'est dans la fiche maison : 4512."},
	}
	note := jeanCarryNote(old)
	if !strings.Contains(note, "User: rappelle-moi le code du portail") || !strings.Contains(note, "Jean: C'est dans la fiche maison") {
		t.Fatalf("reprise : %q", note)
	}
	msg := "[mardi 2026-10-21 08:00 (Paris)] " + note + "et du coup ?"
	if got := jeanCleanUserText(msg); got != "et du coup ?" {
		t.Errorf("la reprise ne doit pas partir au journal : %q", got)
	}
}
