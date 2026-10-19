package ajean

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// web_wol_test.go — les deux routes de l'interface : le préremplissage de la
// modale (les clés WOL y entrent et en ressortent) et l'envoi sur demande
// explicite. Aucun paquet magique réel n'est envoyé (hooks réseau), et rien n'est
// écrit sous le home par défaut (testHome pose AJEAN_HOME dans t.TempDir()).

// TestWOLPresetReadReturnsKeys : la modale qui rouvre un preset doit retrouver la
// MAC et l'attente. Sans ça, l'interrupteur s'affiche décoché sur un preset qui
// réveille, et l'utilisateur croit que le réveil est perdu.
func TestWOLPresetReadReturnsKeys(t *testing.T) {
	testHome(t)
	body := externalPresetContentWOL("http://192.168.0.26:8080/v1", "swift", "", "131072", false,
		wolKeys{Enabled: true, MAC: "b0:82:e2:46:7e:34", Wait: "150"})
	id, err := SavePreset("", "LAN", body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handlePresetExternal(w, httptest.NewRequest("GET", "/api/preset/external?id="+id, nil))
	if w.Code != 200 {
		t.Fatalf("lecture : %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["wol"] != true {
		t.Errorf("wol = %v, attendu true (interrupteur à rendre coché)", out["wol"])
	}
	if out["wol_mac"] != "B082E2467E34" {
		t.Errorf("wol_mac = %v, attendu B082E2467E34", out["wol_mac"])
	}
	if out["wol_wait"] != "150" {
		t.Errorf("wol_wait = %v, attendu 150", out["wol_wait"])
	}
	if out["url"] != "http://192.168.0.26:8080/v1" {
		t.Errorf("url = %v (le champ existant ne doit pas bouger)", out["url"])
	}
	// Un preset sans réveil ne doit rien inventer.
	id2, _ := SavePreset("", "NET", externalPresetContent("http://192.168.0.26:8080/v1", "swift", "", "", false))
	w = httptest.NewRecorder()
	handlePresetExternal(w, httptest.NewRequest("GET", "/api/preset/external?id="+id2, nil))
	out = map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["wol"] != false || out["wol_mac"] != "" {
		t.Errorf("preset sans réveil : wol=%v wol_mac=%v, attendu false et vide", out["wol"], out["wol_mac"])
	}
}

// TestWOLPresetSaveWritesKeys : aller-retour complet par la route d'enregistrement,
// avec la MAC dans la forme d'un utilitaire de LAN (tirets, minuscules).
func TestWOLPresetSaveWritesKeys(t *testing.T) {
	testHome(t)
	save := func(req externalSaveReq) map[string]any {
		b, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		handlePresetExternalSave(w, httptest.NewRequest("POST", "/api/preset/external/save", bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	out := save(externalSaveReq{Name: "LAN", URL: "http://192.168.0.26:8080/v1", Model: "swift",
		WOL: true, WOLMAC: "b0-82-e2-46-7e-34", WOLWait: "150"})
	if out["ok"] != true {
		t.Fatalf("enregistrement refusé : %v", out)
	}
	id, _ := out["id"].(string)
	raw := readPresetOK(t, id)
	cfg := parseEnv(raw)
	if cfg["WOL"] != "1" || cfg["WOL_MAC"] != "B082E2467E34" || cfg["WOL_WAIT"] != "150" {
		t.Errorf("clés écrites : %v", cfg)
	}
	if n := strings.Count(raw, "WOL_MAC="); n != 1 {
		t.Errorf("WOL_MAC écrit %d fois, attendu 1 (une édition ne doit pas dupliquer) :\n%s", n, raw)
	}
	// L'édition qui RETIRE le réveil doit supprimer les clés : un WOL_MAC orphelin
	// avec WOL absent ne doit pas rester dans le fichier.
	out = save(externalSaveReq{ID: id, Name: "LAN", URL: "http://192.168.0.26:8080/v1", Model: "swift"})
	if out["ok"] != true {
		t.Fatalf("édition sans réveil refusée : %v (retirer l'interrupteur doit rester enregistrable)", out)
	}
	raw = readPresetOK(t, id)
	cfg = parseEnv(raw)
	if cfg["WOL"] != "" || cfg["WOL_MAC"] != "" || strings.Contains(raw, "WOL") {
		t.Errorf("clés restées après retrait du réveil : WOL=%q WOL_MAC=%q\n%s", cfg["WOL"], cfg["WOL_MAC"], raw)
	}
	// WOL=1 sans MAC : rien n'est écrit, et la modale est prévenue.
	out = save(externalSaveReq{Name: "X", URL: "http://192.168.0.26:8080/v1", Model: "swift", WOL: true})
	if out["ok"] == true {
		t.Errorf("WOL=1 sans MAC accepté : %v (la modale doit refuser, pas écrire un preset incomplet)", out)
	}
	// WOL=1 sur une API PUBLIQUE : refusé. Autrement le paquet magique partirait sur la
	// diffusion locale de la machine qui fait tourner AJEAN, vers des machines sans
	// rapport avec la cible.
	out = save(externalSaveReq{Name: "OAI", URL: "https://api.openai.com/v1", Model: "gpt-4o-mini",
		WOL: true, WOLMAC: "b0:82:e2:46:7e:34"})
	if out["ok"] == true {
		t.Error("WOL=1 accepté sur api.openai.com : il faut refuser avant d'écrire")
	}
}

// TestWOLSendEndpoint : le bouton « envoyer le paquet magique ». La trame part
// (hooks réseau), la réponse dit ce qui s'est passé, et le garde-fou d'adresse
// s'applique ici comme dans le chat.
func TestWOLSendEndpoint(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSleep = func(time.Duration) {}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})
	var udpSends int
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) { // machine éteinte : port refusé
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) { udpSends++; return &fakeConn{}, nil }

	post := func(body any) map[string]any {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		handlePresetExternalWOL(w, httptest.NewRequest("POST", "/api/preset/external/wol", bytes.NewReader(b)))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("réponse illisible : %v (%s)", err, w.Body.String())
		}
		return out
	}

	// Machine éteinte, MAC valide, cible de LAN : trame + attente, échec honnête
	// (personne ne répond dans ce test), et un message qui se montre tel quel.
	out := post(wolSendReq{MAC: "b0:82:e2:46:7e:34", URL: "http://192.168.0.26:8080/v1", Wait: "10"})
	if out["ok"] == true {
		t.Errorf("réveil déclaré réussi sans machine derrière : %v", out)
	}
	if udpSends == 0 {
		t.Error("aucune trame envoyée : le bouton doit envoyer")
	}
	if e, _ := out["error"].(string); e == "" {
		t.Error("pas de message d'erreur à afficher dans la modale")
	}

	// Cible du test sur AUTRE machine du LAN, avec le port du service pris de l'URL.
	udpSends = 0
	out = post(wolSendReq{MAC: "70:70:fc:06:9a:1c", IP: "192.168.0.13", URL: "http://192.168.0.26:8080/v1", Wait: "10"})
	if udpSends == 0 {
		t.Error("la cible saisie n'a reçu aucune trame")
	}
	if e, _ := out["error"].(string); strings.Contains(e, "publique") {
		t.Errorf("192.168.0.13 refusée comme cible de test : %q", e)
	}

	// Garde-fou : une cible publique est refusée AVANT tout envoi.
	udpSends = 0
	out = post(wolSendReq{MAC: "b0:82:e2:46:7e:34", IP: "8.8.8.8", URL: "http://192.168.0.26:8080/v1"})
	if out["ok"] == true || udpSends != 0 {
		t.Errorf("trame envoyée vers 8.8.8.8 : %v (%d trames)", out, udpSends)
	}

	// MAC invalide : une erreur lisible, pas un envoi muet d'une trame fabriquée.
	udpSends = 0
	out = post(wolSendReq{MAC: "zz:zz", URL: "http://192.168.0.26:8080/v1"})
	if out["ok"] == true || udpSends != 0 || out["error"] == nil {
		t.Errorf("MAC invalide : %v (%d trames)", out, udpSends)
	}

	// Machine allumée : rien à envoyer, et le bouton le dit.
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) { return &fakeConn{}, nil }
	udpSends = 0
	out = post(wolSendReq{MAC: "b0:82:e2:46:7e:34", URL: "http://192.168.0.26:8080/v1"})
	if out["ok"] != true {
		t.Errorf("machine allumée : %v, attendu ok", out)
	}
	if udpSends != 0 {
		t.Errorf("%d trames vers une machine allumée, attendu 0", udpSends)
	}
}

// TestWOLStatusExposesPhase : la bulle de statut lit la phase de réveil de la
// route /api/status. Elle n'existe que pendant un réveil en vol, et seulement
// pour le preset actif : une bulle « réveil du PC… » qui persisterait ferait
// croire que la machine est éteinte alors qu'elle répond.
func TestWOLStatusExposesPhase(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")

	w := httptest.NewRecorder()
	handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	var base map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &base); err != nil {
		t.Fatal(err)
	}
	if v := base["wol_phase"]; v != nil && v != "" {
		t.Errorf("wol_phase = %v hors réveil, attendu vide (une bulle « réveil du PC… » persistante ferait croire à une machine éteinte)", v)
	}

	wolSetPhase("waking", "192.168.0.26", "B082E2467E34")
	w = httptest.NewRecorder()
	handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &base); err != nil {
		t.Fatal(err)
	}
	if base["wol_phase"] != "waking" {
		t.Errorf("wol_phase = %v, attendu waking", base["wol_phase"])
	}
	// Le détail nomme la machine qui se réveille : sur un LAN de plusieurs
	// machines, « réveil du PC… » seul ne dit pas laquelle.
	if d, _ := base["wol_detail"].(string); !strings.Contains(d, "B082E2467E34") {
		t.Errorf("wol_detail = %v, attendu qu'il nomme la cible (MAC)", d)
	}
	// La cible (hôte + MAC) est portée par l'enregistrement de phase, la bulle
	// de statut de l'interface s'en sert.
	if phase, host, _ := wolPhaseActive(); phase != "waking" || host != "192.168.0.26" {
		t.Errorf("enregistrement de phase = %q/%q, attendu waking/192.168.0.26", phase, host)
	}
	wolPhaseClear()
	w = httptest.NewRecorder()
	handleStatus(w, httptest.NewRequest("GET", "/api/status", nil))
	json.Unmarshal(w.Body.Bytes(), &base)
	if v := base["wol_phase"]; v != nil && v != "" {
		t.Errorf("wol_phase = %v après la fin du réveil, attendu vide", v)
	}
	if v := base["wol_detail"]; v != nil && v != "" {
		t.Errorf("wol_detail = %v après la fin du réveil, attendu vide", v)
	}
}

// readPresetOK : le contenu d'un preset, en échouant le test s'il manque.
func readPresetOK(t *testing.T, id string) string {
	t.Helper()
	c, err := ReadPreset(id)
	if err != nil {
		t.Fatalf("lecture du preset %s : %v", id, err)
	}
	return c
}
