package ajean

import "testing"

// Une génération en cours : la tâche ne touche à rien (ni bascule de preset, ni
// passage enregistré) et repasse au prochain tick (#162).
func TestRunTaskBusyLeavesPreset(t *testing.T) {
	testHome(t)
	tk := Task{ID: newTaskID(), Name: "Veille", Prompt: "x", Schedule: "@every 2h", Enabled: true, Preset: "inexistant"}
	if err := saveTask(tk); err != nil {
		t.Fatal(err)
	}
	conv.mu.Lock()
	was := conv.Generating
	conv.Generating = true
	conv.mu.Unlock()
	defer func() {
		conv.mu.Lock()
		conv.Generating = was
		conv.mu.Unlock()
	}()
	runTask(tk)
	got, _ := getTask(tk.ID)
	if got.LastRun != 0 || len(got.History) != 0 {
		t.Fatalf("la tâche a tourné (ou tenté de basculer) pendant une génération : %+v", got.History)
	}
}
