package ajean

// jean_fixes.go — ce qui a réglé une erreur, gardé pour ne jamais la résoudre
// deux fois. Quand une commande échoue et qu'une variante proche marche juste
// après (python3 puis python, curl puis curl.exe), Jean garde la paire dans son
// journal (« correctif »). La fois suivante où la même erreur revient (même
// message, depuis un autre fichier, une autre ligne), le résultat de l'appel raté
// rappelle en UNE ligne ce qui l'avait réglée, avant que le modèle ne cherche de
// nouveau. Une personne qui a rencontré « accès refusé » sur un partage la
// semaine dernière ne retourne pas chercher sur le web.
//
// Le rappel est volontairement court et seulement sur une signature identique :
// un indice approximatif encombrerait le contexte plus qu'il n'aiderait.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	jeanFixKind     = "correctif"
	jeanFixSeen     = 300 // l'erreur telle qu'elle a été vue
	jeanFixChars    = 500 // ce qui l'a réglée
	jeanFixSigChars = 160
	jeanFixHint     = 260
	jeanFixFresh    = 30 * 24 * time.Hour // le même correctif dans ce délai : pas réécrit
)

// jeanFailure : le dernier échec d'une commande, tant qu'aucune n'a marché depuis.
type jeanFailure struct{ cmd, res string }

// bashFailed : le résultat de l'outil bash dit-il un échec ?
func bashFailed(res string) bool {
	if strings.HasPrefix(res, "[erreur") || strings.HasPrefix(res, "[timeout") {
		return true
	}
	if rest, ok := strings.CutPrefix(res, "exit: "); ok {
		return !strings.HasPrefix(rest, "0\n") && rest != "0"
	}
	return false
}

// jeanNoteBashResult suit les commandes d'un tour Jean : un échec reçoit le
// correctif connu (s'il y en a un), un succès qui suit un échec proche est
// gardé comme correctif. Renvoie le résultat, éventuellement complété.
func jeanNoteBashResult(failedAt map[string]jeanFailure, cmd, res string) string {
	if bashFailed(res) {
		failedAt["bash"] = jeanFailure{cmd, res}
		return res + jeanKnownFix(res)
	}
	if f, ok := failedAt["bash"]; ok {
		delete(failedAt, "bash")
		// Seulement une VARIANTE de la commande ratée : la même qui marche la
		// seconde fois n'apprend rien, et une lecture quelconque après un
		// « fichier introuvable » n'en est pas le correctif.
		if squashSpaces(f.cmd) != squashSpaces(cmd) && commandSimilarity(f.cmd, cmd) >= 0.3 {
			jeanLogFix(f.res, "`"+clipRunes(squashSpaces(f.cmd), 200)+"` a échoué ; puis `"+clipRunes(squashSpaces(cmd), 300)+"` a marché")
		}
	}
	return res
}

// commandSimilarity : la part de mots communs à deux commandes, de 0 à 1.
func commandSimilarity(a, b string) float64 {
	wa, wb := map[string]bool{}, map[string]bool{}
	for _, w := range strings.Fields(a) {
		wa[w] = true
	}
	for _, w := range strings.Fields(b) {
		wb[w] = true
	}
	common := 0
	for w := range wa {
		if wb[w] {
			common++
		}
	}
	if n := len(wa) + len(wb) - common; n > 0 {
		return float64(common) / float64(n)
	}
	return 0
}

// jeanLogFix garde ce qui a réglé une erreur : sa signature, l'erreur vue, le
// correctif. Le même correctif pour la même erreur n'est pas réécrit avant 30 jours.
func jeanLogFix(errText, fix string) {
	sig := errorSignature(errText)
	if sig == "" || strings.TrimSpace(fix) == "" {
		return
	}
	fix = clipRunes(fix, jeanFixChars)
	since := time.Now().Add(-jeanFixFresh)
	jeanMu.Lock()
	defer jeanMu.Unlock()
	all := jeanJournalAll()
	for i := len(all) - 1; i >= 0 && all[i].When.After(since); i-- {
		if f, ok := parseJeanFix(all[i].Text); ok && all[i].Kind == jeanFixKind && f.sig == sig && foldSearch(squashSpaces(f.fix)) == foldSearch(squashSpaces(fix)) {
			return // déjà connu, et réglé de la même façon
		}
	}
	text := fmt.Sprintf("Erreur : %s\nVue : %s\nCorrectif : %s", sig, clipRunes(squashSpaces(errorLine(errText)), jeanFixSeen), fix)
	if err := jeanAppendLocked(jeanFixKind, text); err != nil {
		fmt.Fprintf(os.Stderr, "[jean] correctif non écrit : %v\n", err)
	}
}

// jeanKnownFix : "" ou une ligne qui dit ce qui a réglé cette erreur la
// dernière fois (même signature), à ajouter au résultat de l'appel raté.
func jeanKnownFix(errText string) string {
	sig := errorSignature(errText)
	if sig == "" {
		return ""
	}
	jeanMu.Lock()
	all := jeanJournalAll()
	jeanMu.Unlock()
	for i := len(all) - 1; i >= 0; i-- { // le plus récent d'abord : le dernier qui a marché
		if all[i].Kind != jeanFixKind {
			continue
		}
		if f, ok := parseJeanFix(all[i].Text); ok && f.sig == sig {
			return fmt.Sprintf("\n[mémoire] Erreur déjà rencontrée (%s) : %s", all[i].When.In(loc(defaultTaskTZ())).Format("2006-01-02"), clipRunes(squashSpaces(f.fix), jeanFixHint))
		}
	}
	return ""
}

type jeanFixNote struct{ sig, fix string }

func parseJeanFix(text string) (jeanFixNote, bool) {
	var f jeanFixNote
	head, fix, ok := strings.Cut(text, "\nCorrectif : ")
	if !ok {
		return f, false
	}
	f.fix = strings.TrimSpace(fix)
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "Erreur : "); ok {
			f.sig = strings.TrimSpace(v)
		}
	}
	return f, f.sig != ""
}

var (
	// errWordRe : une ligne qui dit ce qui n'a pas marché (repliée), dans les
	// langues dans lesquelles les outils répondent.
	errWordRe = regexp.MustCompile(`error|err!|exception|fail|fatal|denied|refused|cannot|can't|could not|couldn't|unable|invalid|not found|no such|not recognized|undefined|unknown|unexpected|missing|timed out|timeout|forbidden|unauthorized|permission|panic|abort|erreur|echec|impossible|introuvable|refuse|interdit|n'existe pas|n'est pas reconnu|inconnu|manquant`)
	// exitLineRe : ce que l'outil bash ajoute (code de sortie, rubriques).
	exitLineRe = regexp.MustCompile(`^(exit: -?\d+|stdout:|stderr:|\[timeout[^\]]*\])$`)

	// sigKeepRe : les codes qui nomment une erreur. Un 401 et un 404 du même
	// appel, WinError 5 et 32 sont des problèmes différents aux correctifs
	// différents : effacer le code donnerait le correctif de l'un à l'autre.
	sigKeepRe = regexp.MustCompile(`\b(?:http|status|code|errno|exit|error|erreur|winerror)[ :=#]+\d{1,4}\b|\b0x[0-9a-f]{8}\b` +
		`|\bhttp/\d(?:\.\d)? \d{3}\b|\b[1-5]\d\d (?:client|server) error\b|\berror: \([1-5]\d\d\)|\bcurl: \(\d+\)` +
		`|\b(?:cs|ts|lnk|c|e|ora-)\d{3,5}\b`)
	sigURLRe    = regexp.MustCompile(`https?://([^/\s"'<>]+)[^\s"'<>]*`)
	sigUUIDRe   = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	sigQuoteRe  = regexp.MustCompile("\"[^\"\\n]*\"|`[^`\\n]*`|«[^»\\n]*»|“[^”\\n]*”")
	sigSingleRe = regexp.MustCompile(`(^|[\s(\[{:=,])'[^'\n]*'`)
	sigWinRe    = regexp.MustCompile(`\b[a-z]:[\\/][^\s"'<>|\x01]*|\\\\[^\s"'<>|\x01]+`)
	sigPathRe   = regexp.MustCompile(`(^|[\s(=,])(?:~|\.{1,2})?/[^\s"'<>\x01]+`)
	sigRelRe    = regexp.MustCompile(`[\w.-]+(?:[\\/][\w.-]+)+`)
	sigFileRe   = regexp.MustCompile(`\b[\w-]+(?:\.[\w-]+)*\.(?:go|py|js|mjs|cjs|ts|tsx|jsx|json|ya?ml|toml|ini|cfg|conf|txt|md|csv|tsv|log|html?|css|xml|sh|ps1|bat|cmd|exe|dll|so|c|h|cpp|hpp|cs|java|rb|php|rs|sql|db|xlsx?|docx?|pptx?|pdf|png|jpe?g|gif|zip|tar|gz)\b`)
	sigTimeRe   = regexp.MustCompile(`\b\d+(?:[.,]\d+)?(?:h|m|s|ms|us|ns)(?:\d+(?:[.,]\d+)?(?:m|s|ms|us|ns))*\b|\b\d+(?:[.,]\d+)? ?(?:secondes?|seconds?|secs?|minutes?|mins?)\b`)
	sigHexRe    = regexp.MustCompile(`\b0x[0-9a-f]+\b|\b[0-9a-f]{7,}\b`)
	sigNumRe    = regexp.MustCompile(`\d+(?:[.,:]\d+)*`)
	sigMarkRe   = regexp.MustCompile(`<(?:q|path|file|time|hex|id|n)>`)
)

// errorLine : la ligne de la sortie qui dit ce qui n'a pas marché (la dernière
// d'une trace Python, la première du rapport d'un compilateur), sinon la première.
func errorLine(s string) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "[erreur]")); l != "" && !exitLineRe.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	k := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "Traceback (most recent call last)") {
			k = len(lines) - 1 // Python : l'erreur est la dernière ligne, sous le code cité
			break
		}
		if errWordRe.MatchString(foldSearch(l)) {
			k = i
			break
		}
	}
	if line := lines[k]; len([]rune(line)) < 12 && k+1 < len(lines) { // « Error: » avec le message dessous
		return line + " " + lines[k+1]
	}
	return lines[k]
}

// errorSignature : une erreur sans ce qui change d'une fois à l'autre (chemins,
// noms de fichiers, nombres, hexadécimal, valeurs citées, durées), repliée et
// courte : la même erreur dans un autre fichier ou à une autre ligne a la même.
// Les codes qui nomment l'erreur restent (HTTP 404, exit 127, 0x80070005).
func errorSignature(s string) string {
	line := foldSearch(errorLine(s))
	if line == "" {
		return ""
	}
	var kept []string
	line = sigKeepRe.ReplaceAllStringFunc(line, func(k string) string {
		kept = append(kept, k)
		return "\x01" + string(rune('a'+len(kept)-1)) + "\x02" // sans chiffre : les nombres ci-dessous l'épargnent
	})
	line = sigURLRe.ReplaceAllString(line, "$1")
	line = sigUUIDRe.ReplaceAllString(line, "<id>")
	line = sigQuoteRe.ReplaceAllString(line, "<q>")
	line = sigSingleRe.ReplaceAllString(line, "$1<q>")
	line = sigWinRe.ReplaceAllString(line, "<path>")
	line = sigPathRe.ReplaceAllString(line, "$1<path>")
	line = sigRelRe.ReplaceAllString(line, "<path>")
	line = sigFileRe.ReplaceAllString(line, "<file>")
	line = sigTimeRe.ReplaceAllString(line, "<time>")
	line = sigHexRe.ReplaceAllStringFunc(line, func(h string) string {
		if strings.HasPrefix(h, "0x") || strings.ContainsAny(h, "0123456789") && strings.ContainsAny(h, "abcdef") {
			return "<hex>"
		}
		return h
	})
	line = sigNumRe.ReplaceAllString(line, "<n>")
	for i, k := range kept {
		line = strings.Replace(line, "\x01"+string(rune('a'+i))+"\x02", k, 1)
	}
	r := []rune(squashSpaces(line))
	if len(jeanSearchTerms(sigMarkRe.ReplaceAllString(string(r), " "))) == 0 {
		return "" // rien que des valeurs : impossible de distinguer une erreur d'une autre
	}
	return string(r[:min(len(r), jeanFixSigChars)])
}
