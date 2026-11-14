package ajean

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wolPresetPath(t *testing.T, home, id string) string {
	t.Helper()
	return filepath.Join(home, "presets", id+".env")
}

func wolPresetFile(t *testing.T, home, id, content string) string {
	t.Helper()
	dir := filepath.Join(home, "presets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".env")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const wolPresetBody = "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=qwen\nEXTERNAL_KEY=k\nWOL=1\nWOL_MAC=B082E2467E34\n"

// Le chemin du user, en entier : la modale enregistre le preset ACTIF avec le
// réveil armé, et le chat doit avoir la MAC.
func TestWOLSaveActivePresetArmsTheChat(t *testing.T) {
	home := testHome(t)
	defer restoreWOLHooks()

	// 1. Le preset de LAN, SANS réveil, est créé puis APPLIQUÉ : c'est le preset
	//    actif, porteur de la marque d'actif. C'est l'état d'un user qui a un
	//    preset externe du LAN et qui ouvre sa modale.
	noWake := "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=qwen\nEXTERNAL_KEY=k\n"
	wolPresetFile(t, home, "wol", noWake)
	if err := applyPresetFile(wolPresetPath(t, home, "wol")); err != nil {
		t.Fatal(err)
	}
	if _, ok := wolOf(ReadConfig()); ok {
		t.Fatal("départ : la config ne doit pas déclarer de réveil")
	}
	if id := activePresetID(); id != "wol" {
		t.Fatalf("preset actif = %q, attendu wol", id)
	}

	// 3. La modale ENREGISTRE (sans basculer). C'est là que le WOL disparaissait.
	req := externalSaveReq{Name: "wol", ID: "wol", URL: "http://192.168.0.26:8080/v1",
		Model: "qwen", Key: "k", KeyTouched: true, Ctx: "8192", WOL: true, WOLMAC: "b0:82:e2:46:7e:34"}
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	handlePresetExternalSave(rec, httptest.NewRequest("POST", "/api/preset/external/save", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("code = %d : %s", rec.Code, rec.Body.String())
	}
	t.Logf("réponse de la sauvegarde : %s", rec.Body.String())
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if w, ok := out["warning"]; ok {
		t.Fatalf("l'application à la config vivante a échoué : %v", w)
	}

	// 4. Le chat, lui, lit la config vivante : il doit avoir la MAC.
	tt, ok := wolOf(ReadConfig())
	if !ok || tt.MAC != "B082E2467E34" {
		t.Fatalf("config vivante après sauvegarde : MAC %q ok=%v, attendu B082E2467E34", tt.MAC, ok)
	}
	ep := resolveChatEndpoint()
	if ep.WOL.MAC == "" {
		t.Fatal("resolveChatEndpoint : MAC vide, le chat n'enverra rien")
	}
	t.Logf("chat armé : endpoint %s, MAC %s", ep.URL, ep.WOL.MAC)
}

// Un preset NON actif ne doit PAS être appliqué à la config vivante par une
// sauvegarde : la bascule reste un acte explicite.
func TestWOLSavingInactivePresetDoesNotTouchLiveConfig(t *testing.T) {
	home := testHome(t)
	defer restoreWOLHooks()
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.13:8080/v1\nEXTERNAL_MODEL=autre\nEXTERNAL_KEY=k\n")
	wolPresetFile(t, home, "wol", wolPresetBody)

	req := externalSaveReq{Name: "wol", ID: "wol", URL: "http://192.168.0.26:8080/v1",
		Model: "qwen", Key: "k", KeyTouched: true, Ctx: "8192", WOL: true, WOLMAC: "b0:82:e2:46:7e:34"}
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	handlePresetExternalSave(rec, httptest.NewRequest("POST", "/api/preset/external/save", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("code = %d : %s", rec.Code, rec.Body.String())
	}
	cfg := ReadConfig()
	if cfg[extKeyURL] != "http://192.168.0.13:8080/v1" {
		t.Errorf("la config vivante a bougé : %q, attendu l'URL d'avant", cfg[extKeyURL])
	}
	if _, ok := wolOf(cfg); ok {
		t.Error("un preset non actif ne doit pas armer le réveil")
	}
}

// Le chat doit savoir réveiller une cible nommée par son NOM (le cas d'usage
// réel : l'URL d'un preset porte un nom d'hôte), et doit refuser ce qu'il ne
// peut pas faire sans le faire croire tenté.
func TestWOLChatResolvesAHostnameIntoAWakeableAddress(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var looked []string
	lookup := wolLookupHost
	defer func() { wolLookupHost = lookup }()
	wolLookupHost = func(h string) ([]string, error) { looked = append(looked, h); return []string{"192.168.0.26"}, nil }

	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://pc-bureau.local:8080/v1\nEXTERNAL_MODEL=qwen\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")

	h, err := wolWakeHost("pc-bureau.local")
	if err != nil || h != "192.168.0.26" {
		t.Fatalf("wolWakeHost = %q (%v), attendu 192.168.0.26", h, err)
	}
	if len(looked) == 0 {
		t.Error("le nom n'a pas été résolu")
	}

	// Un nom qui ne résout pas : le message doit dire ce qui manque, pas accuser
	// la machine d'être éteinte.
	wolLookupHost = func(string) ([]string, error) { return nil, errors.New("no such host") }
	_, e := wolWakeHost("inconnu")
	if e == nil || !strings.Contains(e.Error(), "adresse IP") {
		t.Errorf("nom irrésolvable : %v, attendu qu'il demande l'IP dans l'URL", e)
	}
	if strings.Contains(fmt.Sprint(e), "ne s'est pas réveillé") {
		t.Error("le message prétend qu'un réveil a échoué")
	}
	// Une adresse publique : refusée, et rien n'est envoyé.
	wolLookupHost = func(string) ([]string, error) { return []string{"203.0.113.9"}, nil }
	if _, e := wolWakeHost("api.openai.com"); e == nil {
		t.Error("une adresse hors LAN doit être refusée")
	}
}
