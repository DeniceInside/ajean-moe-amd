package ajean

import (
	"strings"
	"testing"
)

func TestLightenMessages(t *testing.T) {
	testHome(t)
	if !compactEnabled() {
		t.Skip("compactage coupé dans cette configuration de test")
	}
	var archived []recallArchiveInput
	saved := recallArchive
	recallArchive = func(b []recallArchiveInput) ([]recallEntry, error) {
		archived = append(archived, b...)
		out := make([]recallEntry, len(b))
		for i := range b {
			out[i] = recallEntry{id: "r9"}
		}
		return out, nil
	}
	defer func() { recallArchive = saved }()

	big := "DEBUT " + strings.Repeat("ligne de page web lue. ", 300) + " FIN"
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "cherche la doc" + jeanRecallMark + ". Follow the fiche…]\nFiche x"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunc{Name: "web_read"}}}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "voilà"},
		{Role: "user", Content: "et ensuite ?"},
		{Role: "assistant", Content: "ensuite"},
		{Role: "user", Content: "dernier" + jeanRecallMark + ". x]\nFiche y"},
		{Role: "tool", ToolCallID: "c2", Content: big},
	}
	out, changed := lightenMessages(msgs, Caps{Agent: true})
	if !changed || len(archived) != 1 || archived[0].Content != big {
		t.Fatalf("archive : changed=%v %d", changed, len(archived))
	}
	tool := out[3].Content.(string)
	if !strings.HasPrefix(tool, "DEBUT") || !strings.HasSuffix(tool, "FIN") || !strings.Contains(tool, "recall(r9)") || len(tool) > 1500 {
		t.Errorf("résultat allégé : %d car.\n%.200s", len(tool), tool)
	}
	if out[1].Content != "cherche la doc" {
		t.Errorf("le rappel d'un ancien message doit partir : %q", out[1].Content)
	}
	if out[7].Content != msgs[7].Content || out[8].Content != big {
		t.Error("les deux derniers échanges restent intacts")
	}
	if msgs[3].Content != big {
		t.Error("l'historique reçu ne doit pas être modifié")
	}
	// Sans les outils (terminal) : aucun raccourcissement, l'archive serait inutilisable.
	archived = nil
	out2, _ := lightenMessages(msgs, Caps{Agent: true, Terminal: true})
	if len(archived) != 0 || out2[3].Content != big {
		t.Error("terminal : rien de raccourci")
	}
}
