package ajean

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Les tests ci-dessous ne touchent JAMAIS le réseau réel : net.DialTimeout,
// net.Dial et net.Interfaces sont des variables (wolDial, wolDialUDP,
// wolInterfaces), remplacées ici comme le fait déjà backend_extservice_test.go
// avec extUnitAction. Aucun test n'envoie de paquet magique, et aucun n'écrit
// sous le home par défaut : testHome(t) pose AJEAN_HOME dans t.TempDir().

// fakeNet : une table d'interfaces injectée, pour tester les adresses de diffusion
// sans dépendre de la machine qui fait tourner les tests. Les adresses sont
// rendues par le point d'appel wolAddrsOf (net.Interface.Addrs est une méthode :
// on la remplace, comme net.Interfaces).
var fakeAddrs = map[int][]*net.IPNet{}

// fakeNet installe la table d'interfaces et ses adresses, et rend le point
// d'appel wolInterfaces. Une CIDR mal écrite se voit à la première ligne : la
// fonction rend l'erreur, et le test qui la reçoit échoue sur place.
//
// L'adresse est stockée TELLE QUE GO LA REND : net.Interface.Addrs donne
// l'adresse de l'INTERFACE avec son masque (192.168.0.104/24), pas le réseau
// masqué que rend net.ParseCIDR (192.168.0.0/24). Un réseau masqué fait passer un
// test d'« adresse de cette machine » pour une fiction : wolIsLocalIP ne
// reconnaît que l'adresse d'interface.
func fakeNet(list []string) func() ([]net.Interface, error) {
	fakeAddrs = map[int][]*net.IPNet{}
	var ifas []net.Interface
	var bad error
	for i, s := range list {
		ifa := net.Interface{Index: i + 1, MTU: 1500, Name: fmt.Sprintf("if%d", i), Flags: net.FlagUp | net.FlagBroadcast}
		if strings.HasPrefix(s, "lo:") {
			ifa.Name = "lo"
			ifa.Flags |= net.FlagLoopback
			s = strings.TrimPrefix(s, "lo:")
		}
		for _, cidr := range strings.Split(s, "+") {
			ipn, n, err := net.ParseCIDR(cidr)
			if err != nil {
				bad = fmt.Errorf("CIDR invalide dans le test : %q (%v)", cidr, err)
				continue
			}
			fakeAddrs[ifa.Index] = append(fakeAddrs[ifa.Index], &net.IPNet{IP: ipn, Mask: n.Mask})
		}
		ifas = append(ifas, ifa)
	}
	wolAddrsOf = func(ifa *net.Interface) ([]net.Addr, error) {
		var out []net.Addr
		for _, n := range fakeAddrs[ifa.Index] {
			out = append(out, n)
		}
		return out, nil
	}
	return func() ([]net.Interface, error) {
		if bad != nil {
			return nil, bad
		}
		return ifas, nil
	}
}

func TestWOLNormalizeMAC(t *testing.T) {
	testHome(t)
	ok := map[string]string{
		"b0:82:e2:46:7e:34":   "B082E2467E34", // rendu de `ip link`
		"B0-82-E2-46-7E-34":   "B082E2467E34", // rendu de Windows / getmac
		"b082e2467e34":        "B082E2467E34", // rendu de `wakeonlan`
		" 70-70-fc-06-9a-1c ": "7070FC069A1C",
		"B0.82.E2.46.7E.34":   "B082E2467E34",
	}
	for raw, want := range ok {
		got, err := wolNormalizeMAC(raw)
		if err != nil {
			t.Errorf("wolNormalizeMAC(%q) : %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("wolNormalizeMAC(%q) = %q, attendu %q", raw, got, want)
		}
	}
	for _, raw := range []string{"", "b0:82:e2:46:7e", "b0:82:e2:46:7e:34:aa", "zz:82:e2:46:7e:34", "g082e2467e34", "192.168.0.26"} {
		if _, err := wolNormalizeMAC(raw); err == nil {
			t.Errorf("wolNormalizeMAC(%q) : la MAC est invalide, l'erreur est attendue", raw)
		}
	}
}

func TestWOLMagicPacket(t *testing.T) {
	testHome(t)
	const mac = "B082E2467E34"
	b, err := wolMagicPacket(mac)
	if err != nil {
		t.Fatalf("wolMagicPacket : %v", err)
	}
	if len(b) != 102 {
		t.Fatalf("trame de %d octets, attendu 102 (6 × FF + 16 × MAC)", len(b))
	}
	want := strings.Repeat("ff", 6) + strings.Repeat(strings.ToLower(mac), 16)
	if got := hex.EncodeToString(b); got != want {
		t.Errorf("trame = %s…, attendu %s…", got[:24], want[:24])
	}
	if _, err := wolMagicPacket("ZZZZZZ"); err == nil {
		t.Error("une MAC non hexadécimale doit refuser de construire une trame")
	}
	if _, err := wolMagicPacket("B082E2"); err == nil {
		t.Error("une MAC tronquée doit refuser de construire une trame")
	}
}

func TestWOLBroadcastTargets(t *testing.T) {
	testHome(t)
	defer func() { wolInterfaces = net.Interfaces }()

	// Un /24 et un /16 : les diffusion dirigées attendues sont 192.168.0.255 et
	// 10.0.255.255. Une cible hors de toute interface n'en produit aucune.
	wolInterfaces = fakeNet([]string{
		"192.168.0.26/24",
		"10.0.0.5/16",
		"192.168.56.1/24",
		"lo:127.0.0.1/8",
	})
	targets := wolBroadcastTargets("192.168.0.26")
	want := []string{"255.255.255.255", "192.168.0.26", "192.168.0.255"}
	if strings.Join(targets, " ") != strings.Join(want, " ") {
		t.Errorf("cibles = %v, attendu %v", targets, want)
	}
	if got := wolBroadcastTargets("10.0.7.9"); strings.Join(got, " ") != "255.255.255.255 10.0.7.9 10.0.255.255" {
		t.Errorf("/16 : cibles = %v", got)
	}
	// Hors sous-réseau de chaque interface : aucune diffusion dirigée.
	if got := wolBroadcastTargets("172.16.4.1"); strings.Join(got, " ") != "255.255.255.255 172.16.4.1" {
		t.Errorf("cible hors des interfaces : cibles = %v", got)
	}
	// L'interface loopback ne fournit jamais 127.0.0.255.
	if got := wolBroadcastTargets("127.0.0.1"); strings.Contains(strings.Join(got, " "), "127.0.0.255") {
		t.Errorf("la boucle ne diffuse pas : %v", got)
	}
	// IPv6 : pas de diffusion limitée IPv4, on n'émet que vers la cible.
	if got := wolBroadcastTargets("fe80::1"); len(got) != 1 || got[0] != "fe80::1" {
		t.Errorf("cible IPv6 = %v, attendu [fe80::1]", got)
	}
	// Pas d'interfaces accessibles : les deux destinations de base restent.
	wolInterfaces = func() ([]net.Interface, error) { return nil, errors.New("réseau coupé") }
	if got := wolBroadcastTargets("192.168.0.26"); strings.Join(got, " ") != "255.255.255.255 192.168.0.26" {
		t.Errorf("file d'interfaces en échec = %v", got)
	}
}

func TestWOLGuard(t *testing.T) {
	testHome(t)
	for _, ip := range []string{"192.168.0.26", "10.0.7.9", "172.16.8.1", "169.254.1.2", "127.0.0.1"} {
		if err := wolGuard(ip); err != nil {
			t.Errorf("wolGuard(%q) : %v — une adresse de LAN doit être acceptée", ip, err)
		}
	}
	// Le garde-fou d'adresse : une IP publique, et un nom de domaine (jamais
	// résolu : un nom peut tomber sur une IP publique, et la diffusion serait
	// alors celle de la machine qui fait tourner AJEAN, sans lien avec la cible).
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if err := wolGuard(ip); err == nil {
			t.Errorf("wolGuard(%q) accepte une adresse publique : un paquet magique ne sort pas du LAN", ip)
		}
	}
	for _, host := range []string{"api.openai.com", "localhost", "ajean.local", "mon-pc"} {
		if err := wolGuard(host); err == nil {
			t.Errorf("wolGuard(%q) accepte un nom : la cible doit être une IP littérale", host)
		}
	}
}

func TestWOLHostPort(t *testing.T) {
	testHome(t)
	cases := map[string][2]string{
		"http://192.168.0.26:8080/v1":                  {"192.168.0.26", "8080"},
		"http://192.168.0.26:8080/v1/chat/completions": {"192.168.0.26", "8080"},
		"https://mon-pc.local:8443/v1":                 {"mon-pc.local", "8443"},
		"http://192.168.0.26/v1":                       {"192.168.0.26", "80"},
		"https://example.com/v1":                       {"example.com", "443"},
	}
	for raw, want := range cases {
		h, p, err := wolHostPort(raw)
		if err != nil {
			t.Errorf("wolHostPort(%q) : %v", raw, err)
			continue
		}
		if h != want[0] || p != want[1] {
			t.Errorf("wolHostPort(%q) = %s:%s, attendu %s:%s", raw, h, p, want[0], want[1])
		}
	}
	if _, _, err := wolHostPort("pas une url ://"); err == nil {
		t.Error("une URL invalide doit refuser de donner un hôte")
	}
	if _, _, err := wolHostPort("/v1"); err == nil {
		t.Error("une URL sans hôte doit refuser de donner un port")
	}
}

func TestWOLPorts(t *testing.T) {
	testHome(t)
	if got := wolPorts(7); len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Errorf("wolPorts(7) = %v, attendu [7 9]", got)
	}
	if got := wolPorts(9); got[0] != 9 || got[1] != 7 {
		t.Errorf("wolPorts(9) = %v, attendu [9 7]", got)
	}
	if got := wolPorts(12345); len(got) != 1 || got[0] != 12345 {
		t.Errorf("wolPorts(12345) = %v, attendu [12345] (un port nommé par l'utilisateur n'est pas doublé)", got)
	}
}

func TestWOLOf(t *testing.T) {
	testHome(t)
	// Absent, ou éteint : rien à réveiller.
	for _, cfg := range []map[string]string{
		{"EXTERNAL": "1", "EXTERNAL_URL": "https://api.openai.com/v1"},
		{"EXTERNAL": "1", "EXTERNAL_URL": "http://192.168.0.26:8080/v1", "WOL": "0"},
		{"EXTERNAL": "1", "EXTERNAL_URL": "http://192.168.0.26:8080/v1", "WOL": "1"},                           // pas de MAC
		{"EXTERNAL": "1", "EXTERNAL_URL": "http://192.168.0.26:8080/v1", "WOL": "1", "WOL_MAC": "pas-une-mac"}, // MAC invalide
	} {
		if _, ok := wolOf(cfg); ok {
			t.Errorf("wolOf(%v) : un réveil est annoncé, aucun ne doit l'être", cfg)
		}
	}
	tt, ok := wolOf(map[string]string{"WOL": "1", "WOL_MAC": "b0:82:e2:46:7e:34"})
	if !ok || tt.MAC != "B082E2467E34" || tt.Port != 7 || tt.Wait != 90*time.Second {
		t.Errorf("défauts : %+v ok=%v, attendu MAC B082E2467E34, port 7, attente 90s", tt, ok)
	}
	tt, _ = wolOf(map[string]string{"WOL": "1", "WOL_MAC": "b082e2467e34", "WOL_PORT": "9", "WOL_WAIT": "180"})
	if tt.Port != 9 || tt.Wait != 180*time.Second {
		t.Errorf("clés lues : %+v, attendu port 9, attente 180s", tt)
	}
	// Le plafond dur protège la génération : WOL_WAIT=99999 n'attend pas 27 h.
	tt, _ = wolOf(map[string]string{"WOL": "1", "WOL_MAC": "b082e2467e34", "WOL_WAIT": "99999"})
	if tt.Wait != wolMaxWait {
		t.Errorf("WOL_WAIT=99999 donne %s, attendu le plafond %s", tt.Wait, wolMaxWait)
	}
	// Un port absurdement grand, une attente 0 : les défauts restent.
	tt, _ = wolOf(map[string]string{"WOL": "1", "WOL_MAC": "b082e2467e34", "WOL_PORT": "99999", "WOL_WAIT": "0"})
	if tt.Port != 7 || tt.Wait != 90*time.Second {
		t.Errorf("clés hors bornes : %+v, attendu les défauts 7 / 90s", tt)
	}
}

// TestWOLWakeSendsAndWaits : le parcours complet, sans réseau réel. La cible est
// hors ligne (dial TCP en échec), la trame part vers les destinations calculées,
// le port répond après N sondes, wakeWOL renvoie nil.
func TestWOLWakeSendsAndWaits(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSleep = func(time.Duration) {}

	var sent []string
	var probes int
	wolDial = func(netw, addr string, _ time.Duration) (net.Conn, error) {
		if netw != "tcp" {
			return nil, fmt.Errorf("netw = %s", netw)
		}
		if !strings.HasSuffix(addr, ":8080") {
			return nil, fmt.Errorf("sonde sur %s, attendu le port de l'API", addr)
		}
		probes++
		if probes < 3 { // éteinte, puis éteinte, puis vivante
			return nil, errors.New("connect: connection refused")
		}
		return &fakeConn{}, nil
	}
	wolDialUDP = func(netw, addr string) (net.Conn, error) {
		if netw != "udp" {
			return nil, fmt.Errorf("la trame doit partir en UDP, pas en %s", netw)
		}
		sent = append(sent, addr)
		return &fakeConn{onWrite: func(b []byte) error {
			if len(b) != 102 {
				return fmt.Errorf("trame de %d octets envoyée vers %s, attendu 102", len(b), addr)
			}
			return nil
		}}, nil
	}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})

	start := time.Now()
	err := wakeWOL(context.Background(), "192.168.0.26", "8080", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 30 * time.Second})
	if err != nil {
		t.Fatalf("wakeWOL : %v", err)
	}
	// 2 rafales × 3 destinations × 2 ports (7 et 9).
	if len(sent) != 12 {
		t.Errorf("%d envois, attendu 12 (2 rafales × 3 destinations × 2 ports) : %v", len(sent), sent)
	}
	for _, want := range []string{"255.255.255.255:7", "192.168.0.255:9", "192.168.0.26:7"} {
		if !strings.Contains(strings.Join(sent, " "), want) {
			t.Errorf("la destination %s manque : %v", want, sent)
		}
	}
	if probes < 3 {
		t.Errorf("%d sondes du port, attendu au moins 3 (une avant, deux pendant l'attente)", probes)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("le parcours a duré %s avec un sleep neutralisé", d)
	}
}

// TestWOLNoWakeWhenPortAnswers : le service répond déjà → AUCUNE trame. C'est le
// premier test de la série : un preset dont la machine est allumée ne doit rien
// envoyer, jamais, sur aucun de ses messages.
func TestWOLNoWakeWhenPortAnswers(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var sent []string
	wolDial = func(_, addr string, _ time.Duration) (net.Conn, error) { return &fakeConn{}, nil }
	wolDialUDP = func(_, addr string) (net.Conn, error) {
		sent = append(sent, addr)
		return &fakeConn{}, nil
	}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})

	if err := wakeWOL(context.Background(), "192.168.0.26", "8080", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 5 * time.Second}); err != nil {
		t.Fatalf("wakeWOL : %v", err)
	}
	if len(sent) != 0 {
		t.Errorf("%d paquets envoyés vers un service qui répond déjà : %v", len(sent), sent)
	}
}

// TestWOLGuardBlocksPublic : la cible publique ne reçoit RIEN — ni trame, ni
// sonde suivie d'un envoi. C'est la garde qui empêche un preset API (OpenAI,
// Groq, OpenRouter) d'envoyer un paquet magique sur la diffusion locale.
func TestWOLGuardBlocksPublic(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var dialed, sent []string
	wolDial = func(_, addr string, _ time.Duration) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, addr string) (net.Conn, error) {
		sent = append(sent, addr)
		return &fakeConn{}, nil
	}
	err := wakeWOL(context.Background(), "8.8.8.8", "443", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 2 * time.Second})
	if err == nil {
		t.Fatal("wakeWOL vers une adresse publique doit refuser")
	}
	if len(sent) != 0 {
		t.Errorf("des paquets sont partis vers une adresse publique : %v", sent)
	}
	if len(dialed) != 0 {
		t.Errorf("la cible publique est même sondée (%v) : le garde-fou doit s'appliquer avant", dialed)
	}
	// Un nom non plus : ni trame, ni résolution.
	if err := wakeWOL(context.Background(), "api.openai.com", "443", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: time.Second}); err == nil {
		t.Error("wakeWOL vers un nom doit refuser (la cible est une IP littérale)")
	}
}

// TestWOLTimeoutAndCoolDown : la machine ne démarre pas (Wake-on-LAN désactivé,
// arrêt profond). wakeWOL rend la main, dit quoi vérifier, et le réveil suivant
// de la MÊME cible est refusé sans nouvelle trame : trois onglets ouverts
// n'enverront pas trois rafales en boucle.
func TestWOLTimeoutAndCoolDown(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var sent int
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) {
		sent++
		return &fakeConn{}, nil
	}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})
	tt := wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 4 * time.Second}

	err := wakeWOL(context.Background(), "192.168.0.26", "8080", tt)
	if err == nil {
		t.Fatal("une cible qui ne démarre pas doit rendre une erreur")
	}
	for _, want := range []string{"Power On By PCI-E", "Wake on Magic Packet", "B082E2467E34"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("l'erreur ne mentionne pas %q : %v", want, err)
		}
	}
	first := sent
	if first == 0 {
		t.Error("aucune trame envoyée")
	}
	before := sent
	err = wakeWOL(context.Background(), "192.168.0.26", "8080", tt)
	if err == nil || !strings.Contains(err.Error(), "réveil") {
		t.Errorf("le réveil en refroidissement doit être refusé : %v", err)
	}
	if sent != before {
		t.Errorf("%d trames envoyées pendant le refroidissement (attendu 0, après %d)", sent-before, first)
	}
}

// TestWOLSingleWakePerTarget : deux goroutines sur la même cible → une seule
// rafale. (La même machine vue par deux presets, ou trois onglets ouverts.)
func TestWOLSingleWakePerTarget(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var mu sync.Mutex
	var sent int
	started := make(chan struct{}, 8) // tamponné : la rafale en émet 12, personne ne se bloque
	wolSleep = func(d time.Duration) {
		if d == wolRoundGap {
			mu.Lock()
			if sent >= 3 {
				select {
				case started <- struct{}{}: // la première rafale a bien commencé
				default:
				}
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond) // laisse la seconde goroutine arriver
			return
		}
		time.Sleep(d)
	}
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) {
		mu.Lock()
		sent++
		mu.Unlock()
		return &fakeConn{}, nil
	}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})
	tt := wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 2 * time.Second}

	var wg sync.WaitGroup
	var second string
	wg.Add(2)
	go func() { defer wg.Done(); wakeWOL(context.Background(), "192.168.0.26", "8080", tt) }()
	go func() {
		defer wg.Done()
		<-started
		if err := wakeWOL(context.Background(), "192.168.0.26", "8080", tt); err != nil {
			second = err.Error()
		}
	}()
	wg.Wait()
	if !strings.Contains(second, "déjà en cours") {
		t.Errorf("la seconde requête sur la même cible : %q, attendu « déjà en cours »", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if sent > 12 { // une rafale complète = 2 × 3 × 2
		t.Errorf("%d trames pour deux requêtes simultanées, attendu 12 (une seule rafale)", sent)
	}
}

// TestWOLPhaseAffichee : l'étape de réveil est posée pendant l'attente, et
// effacée à la fin — la bulle de génération doit pouvoir afficher
// « réveil du PC… » comme elle affiche la phase Modal.
func TestWOLPhaseAffichee(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var phases []string
	wolSetPhase = func(phase, host, detail string) { phases = append(phases, phase+"|"+host) }
	wolSleep = func(time.Duration) {}
	var probes int
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		probes++
		if probes < 2 {
			return nil, errors.New("connect: connection refused")
		}
		return &fakeConn{}, nil
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) { return &fakeConn{}, nil }
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})

	if err := wakeWOL(context.Background(), "192.168.0.26", "8080", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 10 * time.Second}); err != nil {
		t.Fatalf("wakeWOL : %v", err)
	}
	joined := strings.Join(phases, " ")
	if !strings.Contains(joined, "waking|192.168.0.26") {
		t.Errorf("phases posées = %v, attendu une étape « waking »", phases)
	}
	if !strings.HasSuffix(joined, "ready|192.168.0.26") {
		t.Errorf("phases posées = %v, attendu une étape finale « ready »", phases)
	}
}

// TestWOLDisabledNeverSends : wolWakeEnabled=false (le chemin d'un simple sondage
// de santé, `ajean test`, l'interface qui sonde) n'envoie JAMAIS de trame, même
// sur une cible éteinte du LAN. C'est la règle déjà posée pour le GPU Cloud.
func TestWOLDisabledNeverSends(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolWakeEnabled = false
	var sent int
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) { sent++; return &fakeConn{}, nil }
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})

	if err := wakeWOL(context.Background(), "192.168.0.26", "8080", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: time.Second}); err == nil {
		t.Error("une cible hors ligne doit rendre une erreur, pas un succès silencieux")
	}
	if sent != 0 {
		t.Errorf("%d trames envoyées avec le réveil désactivé, attendu 0", sent)
	}
	if !wolProbeOnly() {
		t.Error("wolProbeOnly() = false avec wolWakeEnabled=false")
	}
}

// TestWOLPresetKeys : les clés entrent et sortent d'un preset, et ne sont pas
// écrasées par un changement de preset (le piège connu : externalPresetWithMachine
// réécrit HOST/PORT, jamais WOL_*).
func TestWOLPresetKeys(t *testing.T) {
	testHome(t)
	body := externalPresetContent("http://192.168.0.26:8080/v1", "swift", "", "131072", false)
	m := parseEnv(body)
	if m["WOL"] != "" {
		t.Error("un preset externe sans réveil déclaré ne doit porter aucune clé WOL")
	}
	withWOL := body + "\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\nWOL_PORT=9\nWOL_WAIT=120\n"
	m = parseEnv(withWOL)
	tt, ok := wolOf(m)
	if !ok || tt.MAC != "B082E2467E34" || tt.Port != 9 || tt.Wait != 120*time.Second {
		t.Errorf("clés relues depuis le preset : %+v ok=%v", tt, ok)
	}
	// La bascule de preset réécrit HOST/PORT : les clés WOL_* doivent survivre.
	m = parseEnv(externalPresetWithMachine(withWOL, map[string]string{"HOST": "0.0.0.0", "PORT": "8081"}))
	for k, want := range map[string]string{"WOL": "1", "WOL_MAC": "b0:82:e2:46:7e:34", "WOL_PORT": "9", "WOL_WAIT": "120"} {
		if m[k] != want {
			t.Errorf("après bascule de preset, %s = %q, attendu %q", k, m[k], want)
		}
	}
	if tt, ok := wolOf(m); !ok || tt.Port != 9 {
		t.Error("le réveil n'est plus déclaré après la bascule")
	}
}

// restoreWOLHooks remet les points d'appel réseau à leur valeur réelle : aucun
// test ne doit laisser un hook derrière lui (leçon de la CI : un effet de bord
// rendu invisible par un réglage d'environnement).
func restoreWOLHooks() {
	wolDial = net.DialTimeout
	wolAddrsOf = (*net.Interface).Addrs
	fakeAddrs = map[int][]*net.IPNet{}
	wolDialUDP = net.Dial
	wolInterfaces = net.Interfaces
	wolSleep = time.Sleep
	wolWakeEnabled = true
	wolSetPhase = wolPhaseSet
	wolPhaseClear() // aucune étape de réveil ne survit au test
	wolRunIP = func() ([]byte, error) { return exec.Command("ip", "-4", "neigh", "show").CombinedOutput() }
	wolRunARP = func() ([]byte, error) { return exec.Command("arp", "-a").CombinedOutput() }
	wolRunEthtool = func(iface string) ([]byte, error) { return exec.Command("ethtool", iface).CombinedOutput() }
	wolMu.Lock()
	wolWaking = map[string]bool{}
	wolCoolDown = map[string]time.Time{}
	wolMu.Unlock()
}

// fakeConn : une connexion sans réseau, pour les tests de sondes et d'écritures.
type fakeConn struct {
	onWrite func([]byte) error
}

func (c *fakeConn) Write(b []byte) (int, error) {
	if c.onWrite != nil {
		if err := c.onWrite(b); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
func (c *fakeConn) Read(b []byte) (int, error)       { return 0, nil }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// TestWOLEndpointCarriesMAC : la clé du preset arrive jusqu'à l'endpoint de
// complétions — c'est là que doLLM décide s'il réveille.
func TestWOLEndpointCarriesMAC(t *testing.T) {
	testHome(t)
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\nWOL_WAIT=120\n")
	ep := resolveChatEndpoint()
	if !ep.External {
		t.Fatal("le preset doit rester externe")
	}
	if ep.WOL.MAC != "B082E2467E34" || ep.WOL.Wait != 120*time.Second {
		t.Errorf("réveil porté par l'endpoint : %+v", ep.WOL)
	}
	// Un preset API publique n'a rien à réveiller : la MAC reste vide, et le
	// chemin d'appel ne passe pas par le WOL.
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=https://api.openai.com/v1\nEXTERNAL_MODEL=gpt-4o-mini\n")
	if ep := resolveChatEndpoint(); ep.WOL.MAC != "" {
		t.Errorf("un preset OpenAI ne porte pas de MAC, obtenu %q", ep.WOL.MAC)
	}
}

// TestWOLLLMReplaysTheRequest : le parcours demandé, mesuré sur une vraie socket
// HTTP. Le port est d'abord présenté comme fermé (PC éteint) ; la trame part ;
// le port est dit ouvert ; et la REQUÊTE INITIALE est rejouée et aboutit.
// L'utilisateur n'a rien renvoyé : c'est le corps gardé en mémoire qui repart.
func TestWOLLLMReplaysTheRequest(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSleep = func(time.Duration) {}
	wolInterfaces = fakeNet([]string{"127.0.0.1/8"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"réveil"}}]}`)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]

	var sent int
	var probes int
	wolDial = func(_, addr string, _ time.Duration) (net.Conn, error) {
		probes++
		if want := "127.0.0.1:" + port; !strings.HasSuffix(addr, want) {
			return nil, fmt.Errorf("sonde sur %s, attendu le port de l'API (%s)", addr, want)
		}
		if probes == 1 { // la sonde initiale : la machine est éteinte
			return nil, errors.New("connect: connection refused")
		}
		return &fakeConn{}, nil // le port est revenu
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) {
		sent++
		return &fakeConn{}, nil
	}

	body := []byte(`{"model":"swift","messages":[{"role":"user","content":"bonjour"}]}`)
	req, err := http.NewRequest("POST", srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	ep := chatEndpoint{URL: srv.URL + "/v1/chat/completions", Model: "swift", External: true,
		WOL: wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 10 * time.Second}}

	resp, err := doLLM(context.Background(), req, body, ep)
	if err != nil {
		t.Fatalf("doLLM avec réveil : %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("statut %s après réveil, attendu 200 (%s)", resp.Status, b)
	}
	if sent == 0 {
		t.Error("aucune trame envoyée alors que la cible était présentée comme éteinte")
	}
	if probes < 2 {
		t.Errorf("%d sondes, attendu au moins 2 (une avant, une pendant l'attente)", probes)
	}
}

// TestWOLLLMPassThroughWhenAwake : la machine est allumée (le port répond) →
// aucune trame, la requête part directement. C'est le cas de très loin le plus
// fréquent, et il ne doit coûter qu'une sonde.
func TestWOLLLMPassThroughWhenAwake(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	wolDialUDP = func(_, _ string) (net.Conn, error) { sent++; return &fakeConn{}, nil }
	wolInterfaces = fakeNet([]string{"127.0.0.1/8"})

	body := []byte(`{"model":"swift","messages":[]}`)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	ep := chatEndpoint{URL: srv.URL + "/v1/chat/completions", Model: "swift", External: true,
		WOL: wolTarget{MAC: "B082E2467E34", Port: 7, Wait: 5 * time.Second}}
	resp, err := doLLM(context.Background(), req, body, ep)
	if err != nil {
		t.Fatalf("doLLM : %v", err)
	}
	resp.Body.Close()
	if sent != 0 {
		t.Errorf("%d trames envoyées vers une machine allumée, attendu 0", sent)
	}
}

// TestWOLCancelStopsWaiting : une génération annulée pendant l'attente du port
// rend la main tout de suite. Sans ça, un chat interrompu attendrait 90 s de plus.
func TestWOLCancelStopsWaiting(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSleep = func(time.Duration) {}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return nil, errors.New("connect: connection refused")
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) { return &fakeConn{}, nil }

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // génération annulée avant l'attente
	start := time.Now()
	err := wakeWOL(ctx, "192.168.0.26", "8080", wolTarget{MAC: "B082E2467E34", Port: 7, Wait: wolMaxWait})
	if err == nil {
		t.Fatal("un contexte annulé doit rendre une erreur")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("erreur = %v, attendu context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("l'attente a duré %s après annulation, attendu un arrêt immédiat", d)
	}
}

// TestWOLFriendlyError : l'échec de réveil doit se lire comme ce qu'il est — un
// réglage de la machine réveillée — et non comme « le moteur ne répond pas ».
func TestWOLFriendlyError(t *testing.T) {
	testHome(t)
	err := wolFriendlyError(fmt.Errorf("192.168.0.26 n'a pas redémaré"), "192.168.0.26",
		chatEndpoint{WOL: wolTarget{MAC: "B082E2467E34"}})
	msg := err.Error()
	for _, want := range []string{"192.168.0.26", "B082E2467E34", "n'a pas redémaré"} {
		if !strings.Contains(msg, want) {
			t.Errorf("le message d'erreur omet %q : %s", want, msg)
		}
	}
}

// TestWOLPhaseActiveIsBounded : l'étape affichée ne survit pas à son propre
// plafond. Un réveil qui a rendu la main par un chemin inattendu ne doit pas
// laisser l'interface afficher « réveil en cours » sans fin.
func TestWOLPhaseActiveIsBounded(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSetPhase("waking", "192.168.0.26", "B082E2467E34")
	if p, host, _ := wolPhaseActive(); p != "waking" || host != "192.168.0.26" {
		t.Fatalf("étape posée = %q/%q", p, host)
	}
	wolPhaseLive.Lock()
	wolPhaseLive.at = time.Now().Add(-wolMaxWait - time.Minute)
	wolPhaseLive.Unlock()
	if p, _, _ := wolPhaseActive(); p != "" {
		t.Errorf("étape de %s rendue : %q, attendu vide", wolMaxWait, p)
	}
	wolSetPhase("waking", "192.168.0.26", "x")
	wolPhaseClear()
	if p, _, _ := wolPhaseActive(); p != "" {
		t.Errorf("wolPhaseClear inefficace : %q", p)
	}
}
