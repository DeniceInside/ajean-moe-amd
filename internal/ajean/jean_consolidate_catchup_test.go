package ajean

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Une nuit passée sans consolidation alors que la mémoire avait changé avant :
// rattrapage. Une mémoire changée après la dernière nuit attend la suivante (#166).
func TestJeanSleepWindowMissed(t *testing.T) {
	testHome(t)
	rememberUserTZ("Europe/Paris")
	paris := loc("Europe/Paris")
	_ = os.MkdirAll(jeanDir(), 0o700)
	mark := filepath.Join(jeanDir(), jeanConsolidatedMark)
	prof := filepath.Join(jeanDir(), jeanProfileFile)
	_ = os.WriteFile(mark, nil, 0o600)
	_ = os.WriteFile(prof, []byte("- aime le thé\n"), 0o600)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, paris) }
	set := func(p string, tm time.Time) { _ = os.Chtimes(p, tm, tm) }

	now := at(19, 9) // 9 h le 19
	set(mark, at(17, 4))
	set(prof, at(18, 20)) // changée le 18 au soir, la nuit du 19 est passée sans elle
	if !jeanSleepWindowMissed(now) {
		t.Error("nuit du 19 manquée : rattrapage attendu")
	}
	set(prof, at(19, 8)) // changée ce matin : la prochaine nuit suffit
	if jeanSleepWindowMissed(now) {
		t.Error("mémoire changée après la dernière nuit : pas de rattrapage")
	}
	set(prof, at(18, 20))
	set(mark, at(19, 4)) // consolidée dans la nuit
	if jeanSleepWindowMissed(now) {
		t.Error("consolidée dans la nuit : pas de rattrapage")
	}
}

// Le minuteur garde une échéance murale : armé, il a un pas en cours et une
// échéance à l'heure attendue ; réarmé, l'ancien pas ne relance rien.
func TestJeanConsolidateWallDeadline(t *testing.T) {
	jeanReflect.mu.Lock()
	defer func() {
		if jeanConsolidateTimer.t != nil {
			jeanConsolidateTimer.t.Stop()
			jeanConsolidateTimer.t = nil
		}
		jeanReflect.mu.Unlock()
	}()
	jeanConsolidateArmLocked(3 * time.Hour)
	gen := jeanConsolidateTimer.gen
	if jeanConsolidateTimer.t == nil {
		t.Fatal("pas de minuteur")
	}
	if d := time.Until(jeanConsolidateTimer.due) - 3*time.Hour; d > time.Second || d < -time.Second {
		t.Fatalf("échéance décalée de %v", d)
	}
	jeanConsolidateArmLocked(time.Hour)
	if jeanConsolidateTimer.gen == gen {
		t.Fatal("le réarmement doit invalider l'ancien pas")
	}
}
