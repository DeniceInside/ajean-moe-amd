package ajean

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
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

	// Ni URL exploitable, ni IP saisie : la cible est introuvable, et le bouton le dit
	// au lieu d'inventer un port.
	out = post(wolSendReq{MAC: "b0:82:e2:46:7e:34", URL: "pas-une-url"})
	if out["ok"] == true {
		t.Errorf("réveil sans cible : %v", out)
	}
	if e, _ := out["error"].(string); !strings.Contains(e, "IP") {
		t.Errorf("erreur = %q, attendu qu'elle demande l'IP de la cible", e)
	}
	// URL mal formée mais IP saisie : la cible du test garde le port du service.
	// Cible FRAÎCHE (192.168.0.44) : après un réveil sans effet, une cible est en
	// cooldown de 60 s — un réveil par cible, la garde posée. Les cibles des cas
	// précédents (.26, .13) sont donc en cooldown ici, et le message le dit.
	udpSends = 0
	out = post(wolSendReq{MAC: "70:70:fc:06:9a:1c", IP: "192.168.0.44", URL: "pas-une-url", Wait: "10"})
	if udpSends == 0 {
		t.Errorf("cible saisie sans trame (URL mal formée a tout bloqué) : %v", out)
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

// wolTestTimeout : une panne de délai, sans réseau (net.Error avec Timeout()).
type wolTestTimeout struct{}

func (wolTestTimeout) Error() string   { return "i/o timeout" }
func (wolTestTimeout) Timeout() bool   { return true }
func (wolTestTimeout) Temporary() bool { return false }

func wolTestOpErr(inner error) error {
	return &net.OpError{Op: "dial", Net: "tcp",
		Source: &net.TCPAddr{IP: net.IPv4(192, 168, 0, 104)},
		Addr:   &net.TCPAddr{IP: net.IPv4(192, 168, 0, 26), Port: 8080},
		Err:    inner}
}

// Le classeur de panne, en pur : c'est lui qui décide si la sonde de la modale
// propose le paquet magique, et il ne fait aucun réseau. Les trois classes sont
// distinguées parce que le paquet magique n'aide que dans une seule.
func TestWOLWakeHintClassifiesTheFailure(t *testing.T) {
	testHome(t)
	const mac = "b0:82:e2:46:7e:34"
	cases := []struct {
		name string
		err  error
		host string
		on   bool
		mac  string
		want int
	}{
		{"refus = machine allumée, service absent", wolTestOpErr(syscall.ECONNREFUSED), "192.168.0.26", true, mac, wolHintPort},
		{"pas de chemin = machine injoignable", wolTestOpErr(syscall.EHOSTUNREACH), "192.168.0.26", true, mac, wolHintWake},
		{"réseau injoignable", wolTestOpErr(syscall.ENETUNREACH), "192.168.0.26", true, mac, wolHintWake},
		{"délai dépassé", wolTestOpErr(wolTestTimeout{}), "192.168.0.26", true, mac, wolHintWake},
		{"échec TLS : un service écoute, machine allumée", errors.New("remote error: tls: bad record MAC"), "192.168.0.26", true, mac, wolHintNone},
		{"nom introuvable", &net.DNSError{Err: "no such host", Name: "pc-bureau"}, "pc-bureau", true, mac, wolHintNone},
		{"panne réseau hors OpError", errors.New("boum"), "192.168.0.26", true, mac, wolHintNone},
		{"réveil décoché : rien à proposer", wolTestOpErr(syscall.EHOSTUNREACH), "192.168.0.26", false, mac, wolHintNone},
		{"MAC absente : rien à proposer", wolTestOpErr(syscall.EHOSTUNREACH), "192.168.0.26", true, "", wolHintNone},
		{"MAC invalide : rien à proposer", wolTestOpErr(syscall.EHOSTUNREACH), "192.168.0.26", true, "zz:zz:zz:zz:zz:zz", wolHintNone},
		{"cible publique : pas de trame possible", wolTestOpErr(syscall.EHOSTUNREACH), "8.8.8.8", true, mac, wolHintNone},
		{"aucune panne", nil, "192.168.0.26", true, mac, wolHintNone},
	}
	for _, c := range cases {
		if got := wolWakeHint(c.err, c.host, c.on, c.mac); got != c.want {
			t.Errorf("%s : classe %d, attendu %d", c.name, got, c.want)
		}
	}
}

// Le bouton « tester la connexion » sur un port local fermé : refus de connexion,
// donc machine ALLUMÉE. La route doit le dire et NE PAS proposer le paquet
// magique — c'est l'API qu'il faut démarrer. (Port fermé déterministe, aucun
// réseau extérieur, le test passe sur un runner sans LAN.)
func TestWOLExternalTestNamesTheClosedPort(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var udpSends int
	wolDialUDP = func(_, _ string) (net.Conn, error) { udpSends++; return &fakeConn{}, nil }
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	url := "http://127.0.0.1:" + strconv.Itoa(addr.Port) + "/v1"
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL="+url+"\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")
	b, _ := json.Marshal(externalSaveReq{URL: url, Model: "swift", WOL: true, WOLMAC: "b0:82:e2:46:7e:34"})
	w := httptest.NewRecorder()
	handlePresetExternalTest(w, httptest.NewRequest("POST", "/api/preset/external/test", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] == true {
		t.Fatalf("test réussi sur un port fermé : %v", out)
	}
	e, _ := out["error"].(string)
	if !strings.Contains(e, "allumée") {
		t.Errorf("erreur = %q, attendu qu'elle nomme la machine allumée sans service", e)
	}
	if out["wol"] == true {
		t.Errorf("le paquet magique est proposé sur un refus de connexion : %v", out)
	}
	if d, _ := out["detail"].(string); !strings.Contains(d, "refused") {
		t.Errorf("detail = %q, attendu la panne d'origine à côté du message", d)
	}
	// Le test n'a rien envoyé : la sonde ne réveille jamais.
	if udpSends != 0 {
		t.Errorf("%d trame(s) envoyée(s) par la sonde, attendu 0", udpSends)
	}
}

// Le bouton « tester la connexion » vers une cible de LAN INJOIGNABLE, avec le
// réveil coché et une MAC saisie : la panne est injectée (extTestDo), aucun
// réseau. La route doit nommer la machine éteinte, proposer le paquet magique,
// garder la panne d'origine à côté — et n'envoyer RIEN d'elle-même.
func TestWOLExternalTestProposesTheMagicPacket(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var udpSends int
	wolDialUDP = func(_, _ string) (net.Conn, error) { udpSends++; return &fakeConn{}, nil }
	orig := extTestDo
	defer func() { extTestDo = orig }()
	extTestDo = func(*http.Request) (*http.Response, error) {
		return nil, wolTestOpErr(syscall.EHOSTUNREACH)
	}
	url := "http://192.168.0.26:8080/v1"
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL="+url+"\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")
	b, _ := json.Marshal(externalSaveReq{URL: url, Model: "swift", WOL: true, WOLMAC: "b0:82:e2:46:7e:34"})
	w := httptest.NewRecorder()
	handlePresetExternalTest(w, httptest.NewRequest("POST", "/api/preset/external/test", bytes.NewReader(b)))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] == true {
		t.Fatalf("test réussi sur panne injectée : %v", out)
	}
	if out["wol"] != true {
		t.Errorf("paquet magique non proposé : %v", out)
	}
	e, _ := out["error"].(string)
	if !strings.Contains(e, "192.168.0.26") || !strings.Contains(e, "peut-être éteinte") {
		t.Errorf("erreur = %q, attendu la cible nommée et la machine peut-être éteinte", e)
	}
	if d, _ := out["detail"].(string); !strings.Contains(d, "no route to host") {
		t.Errorf("detail = %q, attendu la panne d'origine à côté du message", d)
	}
	if udpSends != 0 {
		t.Errorf("%d trame(s) envoyée(s) par la sonde, attendu 0 : la sonne ne réveille jamais", udpSends)
	}

	// Réveil décoché sur la même panne : rien n'est proposé, le message d'origine reste.
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL="+url+"\nEXTERNAL_MODEL=swift\n")
	b, _ = json.Marshal(externalSaveReq{URL: url, Model: "swift"})
	w = httptest.NewRecorder()
	handlePresetExternalTest(w, httptest.NewRequest("POST", "/api/preset/external/test", bytes.NewReader(b)))
	out = map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out["wol"] == true {
		t.Errorf("paquet magique proposé sans réveil coché : %v", out)
	}
	if e, _ := out["error"].(string); !strings.Contains(e, "injoignable") {
		t.Errorf("erreur = %q, attendu le message d'origine (injoignable)", e)
	}
}

// wolTestARP : une table ARP injectée, sans binaire système.
func wolTestARP(rows ...string) {
	wolRunARP = func() ([]byte, error) { return []byte(strings.Join(rows, "\n")), nil }
	wolRunIP = func() ([]byte, error) { return nil, errors.New("ip absent (test)") }
}

// wolTestNet : les interfaces de la machine qui RÉVEILLE, posées par le test.
// L'émetteur est 192.168.0.104 (celui du LAN réel) ; la cible 192.168.0.26 est une
// AUTRE machine. Les poser est nécessaire : un test qui lit les interfaces réelles
// dépend de la machine qui l'exécute, et celle-ci porte justement 192.168.0.26, qui
// ressortirait « c'est cette machine » au lieu d'être la cible du réveil.
func wolTestNet() {
	wolInterfaces = fakeNet([]string{"lo:127.0.0.1/8", "192.168.0.104/24"})
}

// La MAC d'une cible se lit quand la machine RÉPOND : un test de connexion réussi
// vient de forcer sa résolution ARP, la table la contient. Lecture seule, et une
// absence se dit — elle ne s'invente pas.
func TestWOLLearnMACFromARP(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolTestNet() // la machine qui réveille est 192.168.0.104, pas la cible
	// Format où l'IP est en PREMIER (celui d'`arp -a` sur Windows, que le parseur
	// lit) : sur Linux la table vient de `ip -4 neigh`, testé dans cmd_wol_test.go.
	wolTestARP(
		"192.168.0.26      b0-82-e2-46-7e-34   dynamique",
		"192.168.0.1       bc-24-11-dc-1a-c2   dynamique",
	)
	origLookup := wolLookupHost
	defer func() { wolLookupHost = origLookup }()

	mac, err := wolMACForHost("192.168.0.26")
	if err != nil {
		t.Fatalf("IP de la table : %v", err)
	}
	if mac != "B082E2467E34" {
		t.Errorf("mac = %q, attendu B082E2467E34", mac)
	}

	// Nom de machine : résolu, puis lu.
	wolLookupHost = func(string) ([]string, error) { return []string{"192.168.0.26"}, nil }
	mac, err = wolMACForHost("PC_BUREAU_PAPA")
	if err != nil || mac != "B082E2467E34" {
		t.Errorf("nom résolu : mac = %q, err = %v", mac, err)
	}

	// Absente de la table (cible routée, hors de la liaison) : une erreur, pas une
	// MAC approximative prise sur un voisin.
	if _, err := wolMACForHost("192.168.0.99"); err == nil {
		t.Error("MAC inventée pour une cible absente de la table")
	}

	// Publique : l'entrée ARP d'une adresse publique port-forcée est celle du
	// ROUTEUR. La recopier réveillerait la passerelle.
	if _, err := wolMACForHost("8.8.8.8"); err == nil {
		t.Error("MAC lue pour une adresse publique (c'est le routeur qui répond)")
	}
	// Boucle locale et adresse de CETTE machine : c'est la machine d'AJEAN, elle est
	// déjà allumée, et son adresse n'est pas une cible de réveil. (IsPrivate() vaut
	// false pour 127.0.0.1 : ces deux cas se testent avant le LAN, mesuré.)
	for _, h := range []string{"127.0.0.1", "192.168.0.104"} {
		if _, err := wolMACForHost(h); err == nil || !strings.Contains(err.Error(), "cette machine") {
			t.Errorf("adresse locale %s : err = %v, attendu « c'est cette machine »", h, err)
		}
	}
	// Nom introuvable.
	wolLookupHost = func(string) ([]string, error) { return nil, errors.New("no such host") }
	if _, err := wolMACForHost("pc-inconnu"); err == nil {
		t.Error("MAC lue sur un nom introuvable")
	}
}

// Le test de connexion RÉUSSI remonte la MAC lue, et la modale la préremplit.
// Trois règles : la remonter seulement si le réveil est coché ; ne JAMAIS écraser
// une MAC déjà saisie ; ne rien remonter si la table ne la donne pas.
func TestWOLExternalTestReturnsTheLearnedMAC(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var udpSends int
	wolDialUDP = func(_, _ string) (net.Conn, error) { udpSends++; return &fakeConn{}, nil }
	wolTestNet()
	wolTestARP("192.168.0.26      b0-82-e2-46-7e-34   dynamique")
	orig := extTestDo
	defer func() { extTestDo = orig }()
	extTestDo = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
	}
	url := "http://192.168.0.26:8080/v1"
	postTest := func(body any) map[string]any {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		handlePresetExternalTest(w, httptest.NewRequest("POST", "/api/preset/external/test", bytes.NewReader(b)))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Réveil coché, MAC vide : la MAC apprise est remontée.
	out := postTest(externalSaveReq{URL: url, Model: "swift", WOL: true})
	if out["ok"] != true {
		t.Fatalf("test = %v, attendu ok", out)
	}
	if out["wol_mac"] != "B082E2467E34" {
		t.Errorf("wol_mac = %v, attendu B082E2467E34 (la table ARP contient la cible jointe)", out["wol_mac"])
	}

	// MAC déjà saisie : elle n'est jamais écrasée, rien n'est remonté.
	out = postTest(externalSaveReq{URL: url, Model: "swift", WOL: true, WOLMAC: "aa:bb:cc:dd:ee:ff"})
	if out["ok"] != true {
		t.Fatalf("test = %v, attendu ok", out)
	}
	if m, _ := out["wol_mac"].(string); m != "" {
		t.Errorf("wol_mac = %v, attendu rien : une MAC saisie sur la machine prime", out["wol_mac"])
	}

	// Réveil décoché : rien à apprendre.
	out = postTest(externalSaveReq{URL: url, Model: "swift"})
	if out["ok"] != true {
		t.Errorf("sans réveil coché : %v, attendu ok", out)
	}
	if m, _ := out["wol_mac"].(string); m != "" {
		t.Errorf("wol_mac = %v sans réveil coché, attendu rien", out["wol_mac"])
	}

	// Cible publique (garde-fou), réveil coché : rien n'est remonté, le test passe.
	pub := "https://api.openai.com/v1"
	extTestDo = func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
	}
	out = postTest(externalSaveReq{URL: pub, Model: "gpt", WOL: true})
	if out["ok"] != true {
		t.Errorf("test public = %v, attendu ok", out)
	}
	if m, _ := out["wol_mac"].(string); m != "" {
		t.Errorf("wol_mac = %v sur une cible publique, attendu rien (la table ARP donne le routeur)", out["wol_mac"])
	}
	if udpSends != 0 {
		t.Errorf("%d trame(s) envoyée(s) par le test, attendu 0", udpSends)
	}
}
