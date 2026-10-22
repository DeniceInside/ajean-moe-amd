package ajean

// chat_tools_output.go — la sortie d'une commande telle que le modèle la lit :
// sans les codes qu'un terminal interpréterait (couleurs,
// déplacements du curseur, barre de progression redessinée cent fois avec \r :
// npm et pip en remplissaient le résultat) et, quand elle est trop longue, son
// DÉBUT et sa FIN, la sortie entière étant gardée dans un fichier que le modèle
// peut fouiller (grep) au lieu de relancer la commande.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	cmdOutputHead = 3000 // caractères gardés au début d'une sortie trop longue (le reste : la fin)
	keptCmdOutput = 10   // sorties complètes gardées sur disque ; les plus anciennes partent
	cmdOutputGlob = "ajean-sortie-*.txt"
)

// cmdOutput : s nettoyée des codes de terminal, puis bornée à toolMaxOutput
// caractères (début + fin) avec la sortie entière gardée dans un fichier.
func cmdOutput(s string) string {
	s = cleanTermOutput(s)
	if utf8.RuneCountInString(s) <= toolMaxOutput {
		return s
	}
	how := "filtre la sortie (grep, head, tail) pour voir le milieu"
	if p := saveCmdOutput(s); p != "" {
		how = "la sortie entière est dans " + filepath.ToSlash(p) + " : cherche dedans (grep) au lieu de relancer la commande"
	}
	r := []rune(s)
	head, tail := string(r[:cmdOutputHead]), string(r[len(r)-(toolMaxOutput-cmdOutputHead):])
	return head + fmt.Sprintf("\n… [%d caractères coupés : %s] …\n", len(r)-toolMaxOutput, how) + tail
}

// cleanTermOutput : s sans séquences d'échappement de terminal (CSI, OSC et les
// autres) ni caractères de contrôle hors sauts de ligne et tabulations ; une
// ligne redessinée avec \r ne garde que son dernier dessin.
func cleanTermOutput(s string) string {
	dirty := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			dirty = true
			break
		}
	}
	if !dirty {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == 0x1b:
			i = termEscapeEnd(s, i)
		case c == '\b': // une roue (« |\b/\b- ») ou une surfrappe : le caractère d'avant part
			if n := len(b); n > 0 && b[n-1] != '\n' && b[n-1] != '\r' {
				_, w := utf8.DecodeLastRune(b)
				b = b[:n-w]
			}
			i++
		case c == '\n' || c == '\t' || c == '\r':
			b = append(b, c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		default:
			b = append(b, c)
			i++
		}
	}
	s = string(b)
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if !strings.Contains(l, "\r") {
			continue
		}
		last := ""
		for _, seg := range strings.Split(l, "\r") {
			if seg != "" {
				last = seg
			}
		}
		lines[i] = last
	}
	return strings.Join(lines, "\n")
}

// termEscapeEnd : où finit la séquence d'échappement qui commence en s[i] (ESC).
func termEscapeEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch c := s[i+1]; {
	case c == '[': // CSI : des paramètres, puis un octet final
		j := i + 2
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1
		}
		return j
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_': // OSC et chaînes : jusqu'à BEL ou ESC \
		for j := i + 2; j < len(s); j++ {
			switch {
			case s[j] == 0x07:
				return j + 1
			case s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\':
				return j + 2
			case s[j] == '\n': // jamais refermée : la ligne n'est pas mangée
				return j
			}
		}
		return len(s)
	case c >= 0x20 && c <= 0x2f: // un choix de jeu de caractères (ESC ( B) et assimilés
		j := i + 1
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		return min(j+1, len(s))
	case c < 0x20 || c >= 0x80: // un ESC isolé : le saut de ligne ou le « é » qui suit reste
		return i + 1
	}
	return i + 2
}

// saveCmdOutput écrit s dans un nouveau fichier du dossier temporaire et renvoie
// son chemin, "" en cas d'échec.
func saveCmdOutput(s string) string {
	f, err := os.CreateTemp("", cmdOutputGlob)
	if err != nil {
		return ""
	}
	_, err = f.WriteString(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return ""
	}
	pruneCmdOutputs()
	return f.Name()
}

// pruneCmdOutputs supprime les sorties gardées sauf les keptCmdOutput plus récentes.
func pruneCmdOutputs() {
	files, _ := filepath.Glob(filepath.Join(os.TempDir(), cmdOutputGlob))
	if len(files) <= keptCmdOutput {
		return
	}
	type saved struct {
		path string
		at   int64
	}
	var all []saved
	for _, p := range files {
		if st, err := os.Stat(p); err == nil {
			all = append(all, saved{p, st.ModTime().UnixNano()})
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].at > all[b].at })
	for _, f := range all[min(len(all), keptCmdOutput):] {
		os.Remove(f.path)
	}
}
