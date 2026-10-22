package ajean

// chat_compact_light.go — l'ALLÈGEMENT, passe douce avant le résumé.
//
// Quand le fil atteint le seuil de compactage, on retire d'abord ce qui coûte le
// moins à perdre, sans appeler le modèle : dans les anciens messages seulement
// (les deux derniers échanges restent intacts), les blocs de mémoire rappelés
// pour d'anciens messages (la mémoire les redonne au besoin), puis les longs
// résultats d'outils, ramenés à leur début et leur fin. Une personne garde ses
// notes des pages qu'elle a lues, pas les pages. Si ça suffit à repasser
// nettement sous le seuil, le résumé (qui fait travailler le modèle et perd du
// détail) n'a pas lieu.
//
// Rien n'est perdu : un résultat raccourci est archivé mot pour mot (recall),
// exactement comme le fait le compactage. Sans les outils (mode terminal), on ne
// raccourcit rien, le modèle ne pourrait pas relire l'archive.

import (
	"fmt"
	"strings"
)

const (
	lightToolMax  = 2000 // un résultat d'outil plus long est allégé
	lightToolHead = 600
	lightToolTail = 400
	lightMark     = "[… résultat allégé :"
)

// lightProtectFrom : le début de la queue gardée intacte, les deux derniers
// messages utilisateur et ce qui suit. -1 s'il n'y a pas d'ancien message.
func lightProtectFrom(msgs []Message) int {
	users := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			if users++; users == 2 {
				return i
			}
		}
	}
	return -1
}

// lightenMessages : msgs allégé, et true s'il a changé. Les messages modifiés
// sont copiés : l'historique reçu n'est jamais touché.
func lightenMessages(msgs []Message, caps Caps) ([]Message, bool) {
	stop := lightProtectFrom(msgs)
	if stop <= 0 {
		return msgs, false
	}
	canArchive := caps.Agent && !caps.Terminal && compactEnabled()
	names := torsoToolNames(msgs[:stop])
	out := append([]Message(nil), msgs...)
	changed := false
	type pending struct {
		i          int
		head, tail string
		n          int
	}
	var shrink []pending
	var inputs []recallArchiveInput
	for i := 0; i < stop; i++ {
		m := out[i]
		s, ok := m.Content.(string)
		if !ok {
			continue
		}
		switch {
		case m.Role == "user" && strings.Contains(s, jeanRecallMark):
			// Le rappel d'un ancien message : sa mémoire est encore là, elle le redonnera.
			out[i].Content = s[:strings.Index(s, jeanRecallMark)]
			changed = true
		case m.Role == "tool" && canArchive && !strings.Contains(s, lightMark):
			r := []rune(s)
			if len(r) <= lightToolMax {
				continue
			}
			shrink = append(shrink, pending{i, string(r[:lightToolHead]), string(r[len(r)-lightToolTail:]), len(r) - lightToolHead - lightToolTail})
			inputs = append(inputs, recallArchiveInput{Label: recallLabel(m.Role, names[m.ToolCallID], s), Role: m.Role, Content: s})
		}
	}
	if len(inputs) > 0 {
		if entries, err := recallArchive(inputs); err == nil && len(entries) == len(shrink) {
			for k, p := range shrink {
				out[p.i].Content = p.head + fmt.Sprintf("\n%s %d caractères retirés du contexte ; texte entier : recall(%s)]\n", lightMark, p.n, entries[k].id) + p.tail
				changed = true
			}
		}
	}
	return out, changed
}

// lightenEnough : l'allègement suffit-il à éviter le résumé ? Il faut repasser
// NETTEMENT sous le seuil (l'estimation ne voit ni le prompt système ni le
// gabarit) et avoir vraiment allégé (20 % au moins).
func lightenEnough(before, after []Message) bool {
	b, a := estimateTokens(before), estimateTokens(after)
	return a*10 <= compactThreshold()*7 && a*5 <= b*4
}
