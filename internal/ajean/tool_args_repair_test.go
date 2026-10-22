package ajean

import (
	"encoding/json"
	"testing"
)

// Les erreurs habituelles d'un petit modèle dans les arguments d'un appel sont
// réparées ; un appel coupé ou illisible reste refusé.
func TestRepairToolArgs(t *testing.T) {
	for raw, want := range map[string]string{
		"```json\n{\"command\": \"ls -la\"}\n```":       "ls -la",
		`{"command": "ls -la",}`:                        "ls -la",
		`"{\"command\": \"ls -la\"}"`:                   "ls -la",
		`Voici l'appel : {"command": "ls -la"}`:         "ls -la",
		"{\"command\": \"echo a\nb\"}":                  "echo a\nb",
		`{'command': 'ls -la'}`:                         "ls -la",
		`{'command': 'echo "it\'s"'}`:                   `echo "it's"`,
		`{"command": "ls -la"`:                          "ls -la",
		`{"command": "ls -la", "timeout": 30, "x": [1,`: "ls -la",
	} {
		fixed, ok := repairToolArgs(raw)
		var a map[string]any
		if !ok || json.Unmarshal([]byte(fixed), &a) != nil || a["command"] != want {
			t.Errorf("%q : %q %v, attendu command %q", raw, fixed, ok, want)
		}
	}
	if fixed, ok := repairToolArgs(`{"recursive": True, "path": None}`); !ok || fixed != `{"path":null,"recursive":true}` {
		t.Errorf("littéraux Python : %q %v", fixed, ok)
	}
	if fixed, ok := repairToolArgs(`{"text": "True story, None of it",}`); !ok || fixed != `{"text":"True story, None of it"}` {
		t.Errorf("les mots dans une chaîne restent : %q %v", fixed, ok)
	}
	for _, raw := range []string{`pas du json du tout`, `{"path": "a.txt", "content": "début du fichi`} {
		if fixed, ok := repairToolArgs(raw); ok {
			t.Errorf("%q ne doit pas être réparé (illisible ou coupé) : %q", raw, fixed)
		}
	}
}
