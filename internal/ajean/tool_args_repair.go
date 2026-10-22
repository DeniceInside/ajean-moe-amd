package ajean

// tool_args_repair.go — les arguments d'appel d'outil qu'un petit modèle écrit
// presque bien, réparés au lieu d'être refusés.
//
// Chaque refus coûte un aller-retour complet, et le modèle réécrit souvent
// l'appel de travers à l'identique (mesuré : une virgule finale
// refusée trois fois de suite). On répare donc les erreurs habituelles : bloc
// Markdown autour du JSON, texte autour de l'objet, JSON encodé deux fois,
// virgule avant une fermeture, saut de ligne brut dans une chaîne, guillemets
// simples, True/False/None de Python, accolade fermante manquante.
//
// Un appel COUPÉ (une chaîne encore ouverte à la fin, typiquement à la limite
// de longueur) n'est jamais « réparé » : refermé, il écrivait la moitié d'un
// fichier et répondait [ok].

import (
	"encoding/json"
	"strings"
)

// repairToolArgs : raw réparé en objet JSON valide, re-sérialisé. ok=false
// quand aucune réparation simple ne suffit ou que l'appel a été coupé.
func repairToolArgs(raw string) (string, bool) {
	obj, ok := repairArgsObject(raw)
	if !ok {
		return "", false
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// repairArgsObject : s réparé en objet JSON, quand une réparation suffit.
func repairArgsObject(s string) (map[string]any, bool) {
	try := func(s string) (map[string]any, bool) {
		a := map[string]any{}
		return a, json.Unmarshal([]byte(s), &a) == nil
	}
	s = stripArgsFence(strings.TrimSpace(s))
	if a, ok := try(s); ok {
		return a, true
	}
	// Encodé deux fois : une chaîne JSON qui contient l'objet.
	var inner string
	if strings.HasPrefix(s, `"`) && json.Unmarshal([]byte(s), &inner) == nil {
		s = strings.TrimSpace(inner)
		if a, ok := try(s); ok {
			return a, true
		}
	}
	// Du texte avant ou après l'objet (« Voici l'appel : {...} »).
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 && strings.Count(s[j:], `"`) == 0 {
		s = s[:j+1]
	}
	if a, ok := try(s); ok {
		return a, true
	}
	if !strings.HasPrefix(s, "{") {
		return nil, false
	}
	fixed, cut := sanitizeArgs(s)
	if cut {
		return nil, false
	}
	return try(fixed)
}

// stripArgsFence retire un bloc de code Markdown autour de s.
func stripArgsFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && !strings.ContainsAny(s[:nl], "{[\"") {
		s = s[nl+1:] // l'étiquette de langage
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// sanitizeArgs réécrit du presque-JSON en JSON, en une passe : dans les chaînes,
// les caractères de contrôle bruts sont échappés et les chaînes entre guillemets
// simples passent entre guillemets doubles ; hors des chaînes, les virgules
// finales disparaissent, les littéraux Python deviennent ceux de JSON, et ce qui
// est resté ouvert est refermé à la fin. cut : une chaîne était encore ouverte.
func sanitizeArgs(s string) (out string, cut bool) {
	var b strings.Builder
	var stack []byte // les fermetures attendues : } ou ]
	quote := byte(0) // le délimiteur de la chaîne en cours, 0 hors chaîne
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch {
			case c == '\\' && i+1 < len(s):
				if quote == '\'' && s[i+1] == '\'' {
					b.WriteByte('\'') // \' vaut ' dans une chaîne entre guillemets simples
				} else {
					b.WriteByte(c)
					b.WriteByte(s[i+1])
				}
				i++
			case c == quote:
				b.WriteByte('"')
				quote = 0
			case c == '"': // un guillemet double dans une chaîne entre guillemets simples
				b.WriteString(`\"`)
			case c == '\n':
				b.WriteString(`\n`)
			case c == '\r':
				b.WriteString(`\r`)
			case c == '\t':
				b.WriteString(`\t`)
			default:
				b.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			b.WriteByte('"')
		case '{':
			stack = append(stack, '}')
			b.WriteByte(c)
		case '[':
			stack = append(stack, ']')
			b.WriteByte(c)
		case '}', ']':
			if n := len(stack); n > 0 {
				stack = stack[:n-1]
			}
			b.WriteByte(c)
		case ',':
			// Une virgule juste avant une fermeture disparaît.
			j := i + 1
			for j < len(s) && strings.IndexByte(" \t\r\n", s[j]) >= 0 {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			b.WriteByte(c)
		default:
			if py, js, ok := pyArgsLiteral(s, i); ok {
				b.WriteString(js)
				i += len(py) - 1
				continue
			}
			b.WriteByte(c)
		}
	}
	out = strings.TrimRight(b.String(), " \t\r\n,")
	for i := len(stack) - 1; i >= 0; i-- {
		out += string(stack[i])
	}
	return out, quote != 0
}

// pyArgsLiteral : un littéral Python (mot entier) qui commence en s[i].
func pyArgsLiteral(s string, i int) (py, js string, ok bool) {
	if i > 0 && isArgsWordByte(s[i-1]) {
		return "", "", false
	}
	for _, l := range [][2]string{{"True", "true"}, {"False", "false"}, {"None", "null"}} {
		if strings.HasPrefix(s[i:], l[0]) && (i+len(l[0]) == len(s) || !isArgsWordByte(s[i+len(l[0])])) {
			return l[0], l[1], true
		}
	}
	return "", "", false
}

func isArgsWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
