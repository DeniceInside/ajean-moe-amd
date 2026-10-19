package ajean

import (
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cmd_wol_test.go — `ajean wol`. Aucun test n'exécute ip/arp/ethtool (les appels
// sont des variables wolRunIP, wolRunARP, wolRunEthtool), aucun n'envoie de
// paquet magique, aucun n'écrit sous le home par défaut (testHome pose AJEAN_HOME
// dans t.TempDir()).

func TestWOLARPListFromIpNeigh(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolRunIP = func() ([]byte, error) {
		return []byte(`192.168.0.26 dev enp6s0 lladdr b0:82:e2:46:7e:34 REACHABLE
192.168.0.1 dev enp6s0 lladdr aa:aa:aa:aa:aa:aa REACHABLE
8.8.8.8 dev enp6s0 lladdr bb:bb:bb:bb:bb:bb REACHABLE
192.168.0.99 dev enp6s0 FAILED
fe80::1 dev enp6s0 lladdr cc:cc:cc:cc:cc:cc REACHABLE
`), nil
	}
	peers, err := wolARPTable()
	if err != nil {
		t.Fatalf("wolARPTable : %v", err)
	}
	got := map[string]string{}
	for _, p := range peers {
		got[p.ip] = p.mac
	}
	if len(peers) != 2 {
		t.Fatalf("%d voisins, attendu 2 (les publiques, les FAILED et les IPv6 hors liste) : %+v", len(peers), peers)
	}
	if got["192.168.0.26"] != "B082E2467E34" {
		t.Errorf("MAC de 192.168.0.26 = %q, attendu B082E2467E34", got["192.168.0.26"])
	}
	if _, ok := got["8.8.8.8"]; ok {
		t.Error("une adresse publique est retenue : la liste doit rester celle du LAN")
	}
}

// Sur un système sans `ip` (Windows, macOS), c'est `arp -a` qui rend la liste.
func TestWOLARPListFromArpA(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolRunIP = func() ([]byte, error) { return nil, errors.New("ip : introuvable") }
	wolRunARP = func() ([]byte, error) {
		return []byte(`Interface: 192.168.0.26 --- 0xa
  Adresse IP      Adresse physique      Type
192.168.0.26      b0-82-e2-46-7e-34   dynamique
192.168.0.1       aa-aa-aa-aa-aa-aa   dynamique
224.0.0.252       01-00-5e-00-00-fa   statique
`), nil
	}
	peers, err := wolARPTable()
	if err != nil {
		t.Fatalf("wolARPTable (arp -a) : %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("%d voisins, attendu 2 : %+v", len(peers), peers)
	}
	if peers[0].ip != "192.168.0.26" || peers[0].mac != "B082E2467E34" {
		t.Errorf("voisin = %+v, attendu 192.168.0.26 / B082E2467E34 (les tirets de Windows normalisés)", peers[0])
	}
	for _, p := range peers {
		if strings.HasPrefix(p.ip, "224.") {
			t.Errorf("une adresse multicast est retenue : %+v", p)
		}
	}
}

func TestWOLARPListBothToolsAbsent(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolRunIP = func() ([]byte, error) { return nil, errors.New("ip: command not found") }
	wolRunARP = func() ([]byte, error) { return nil, errors.New("arp: command not found") }
	if _, err := wolARPTable(); err == nil {
		t.Fatal("aucun outil ARP : une erreur est attendue, pas une liste vide muette")
	}
}

// La cible de `ajean wol` vient de l'URL du preset actif ; -ip en désigne une
// autre du même LAN, avec le même port d'API.
func TestWOLTargetHostFromPresetURL(t *testing.T) {
	testHome(t)
	cfg := parseEnv("EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\n")
	host, port, err := wolTargetHost("", "", cfg)
	if err != nil || host != "192.168.0.26" || port != "8080" {
		t.Errorf("cible du preset = %s:%s (%v), attendu 192.168.0.26:8080", host, port, err)
	}
	host, port, err = wolTargetHost("192.168.0.13", "", cfg)
	if err != nil || host != "192.168.0.13" || port != "8080" {
		t.Errorf("-ip = %s:%s (%v), attendu 192.168.0.13:8080 (le port de l'API du preset)", host, port, err)
	}
	host, port, err = wolTargetHost("192.168.0.13", "8090", cfg)
	if err != nil || host != "192.168.0.13" || port != "8090" {
		t.Errorf("-port = %s:%s (%v), attendu 192.168.0.13:8090", host, port, err)
	}
	// Aucun preset externe, aucun -ip : une erreur qui dit quoi faire.
	if _, _, err := wolTargetHost("", "", map[string]string{}); err == nil {
		t.Error("aucune cible et pas de -ip : l'erreur est attendue")
	}
}

// `ajean wol` sans MAC et sans clé WOL_MAC : la commande le dit, et ne part pas
// à l'aventure.
func TestWOLSendNeedsAMAC(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\n")
	err := wolSend(nil)
	if err == nil || !strings.Contains(err.Error(), "WOL_MAC") {
		t.Errorf("erreur = %v, attendu qu'elle nomme la clé WOL_MAC", err)
	}
	// Le MAC de la ligne de commande est normalisé comme celui du preset.
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")
	tt, ok := wolOf(ReadConfig())
	if !ok {
		t.Fatal("le preset doit déclarer un réveil")
	}
	got, err := wolTargetFrom("70-70-fc-06-9a-1c", tt)
	if err != nil || got.MAC != "7070FC069A1C" {
		t.Errorf("MAC de la ligne de commande = %q (%v), attendu 7070FC069A1C", got.MAC, err)
	}
	if got.Port != 7 {
		t.Errorf("port UDP = %d, attendu le défaut 7", got.Port)
	}
	if _, err := wolTargetFrom("zz:zz:zz:zz:zz:zz", tt); err == nil {
		t.Error("un MAC invalide en ligne de commande doit refuser")
	}
}

// Le réglage de réveil du pilote : l'ANALYSE de la sortie d'ethtool, seule chose
// portable et donc testable partout. (Un runner macOS n'a ni ethtool ni interface
// privée : y appeler la lecture réelle rend le test rouge là-bas, et c'est ce qui
// s'est passé au premier run de CI. La porte d'accès, elle, est testée séparément.)
func TestWOLWakeOnFlagsParsing(t *testing.T) {
	testHome(t)
	cases := []struct {
		out  string
		want bool
		ok   bool // ok=false : la sortie ne dit rien du réveil → une erreur
	}{
		{"eth0: ethtool\n\tSettings for eth0:\n\tSupported ports: [ TP ]\n\tWake-on: g\n", true, true},
		{"eth0: ethtool\n\tWake-on: d\n", false, true},
		{"eth0: ethtool\n\tWake-on: dg\n", true, true},
		{"eth0: ethtool\n\tWake-on: dgu\n", true, true},
		{"eth0: ethtool\n\tCurrent message level:\n\tWake-on: (g)\n", true, true},
		{"eth0: ethtool\n\tSupported ports: [ TP ]\n", false, false},
	}
	for _, c := range cases {
		on, err := wolParseWakeOn([]byte(c.out))
		if !c.ok {
			if err == nil {
				t.Errorf("sortie %q : aucune erreur, attendu une erreur (ne pas inventer un état)", c.out)
			}
			continue
		}
		if err != nil {
			t.Errorf("sortie %q : %v (attendu lu : %v)", c.out, err, c.want)
			continue
		}
		if on != c.want {
			t.Errorf("sortie %q : wake-on = %v, attendu %v", c.out, on, c.want)
		}
	}
}

// La porte d'accès au pilote local : Linux lit via ethtool, les autres systèmes
// rendent une erreur qui DONNE la commande à faire à la main. Un échec de lecture
// (non-root) rend une erreur, jamais un faux « désarmé » qui ferait croire à un
// réglage à changer.
func TestWOLLocalWakeSettingGateIsPerOS(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolRunEthtool = func(string) ([]byte, error) { return []byte("Wake-on: g\n"), nil }
	on, err := wolLocalWakeSetting()
	if runtime.GOOS == "linux" {
		t.Logf("linux : wake-on lu = %v (%v) — selon les droits de l'appelant", on, err)
	} else {
		if err == nil {
			t.Errorf("%s : une lecture de pilote est annoncée, attendu une erreur qui donne la commande à faire", runtime.GOOS)
		}
		if !strings.Contains(err.Error(), runtime.GOOS) {
			t.Errorf("message d'erreur = %q, attendu qu'il nomme le système", err)
		}
	}
	wolRunEthtool = func(string) ([]byte, error) {
		return []byte("netlink error: Operation not permitted"), errors.New("exit status 1")
	}
	if _, err := wolLocalWakeSetting(); err == nil {
		t.Error("ethtool en échec doit rendre une erreur, pas un faux état")
	}
}

// wolIsLocalIP : le réglage du pilote ne se lit que sur la machine qui porte la
// carte.
func TestWOLIsLocalIP(t *testing.T) {
	testHome(t)
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("aucune interface réseau")
	}
	for _, ifa := range ifaces {
		addrs, err := ifa.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if !wolIsLocalIP(n.IP) {
				t.Errorf("l'adresse %s de l'interface %s n'est pas reconnue comme locale", n.IP, ifa.Name)
			}
		}
	}
	if wolIsLocalIP(net.ParseIP("198.51.100.7")) {
		t.Error("une adresse publique n'est pas locale")
	}
}

// Le texte de `ajean wol check` doit nommer les trois couches, sur chaque
// plateforme : c'est là que le WOL échoue, et le dire est l'essentiel de la
// commande.
func TestWOLCheckNamesTheThreeLayers(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")
	err := wolCheck([]string{"192.168.0.26"})
	if err != nil {
		t.Fatalf("wolCheck : %v", err)
	}
	// Une cible hors LAN est refusée avant tout affichage.
	if err := wolCheck([]string{"8.8.8.8"}); err == nil {
		t.Error("wolCheck vers une adresse publique doit refuser")
	}
	// Aucun preset externe, aucun argument : une erreur qui donne l'usage.
	setConfig(t, "BIN=/opt/llama/llama-server\nMODEL=Qwen3.gguf\n")
	if err := wolCheck(nil); err == nil {
		t.Error("aucune cible et pas d'argument : l'erreur est attendue")
	}
}

// TestWOLSendEndToEnd : le parcours de la commande, sans réseau réel. Le port est
// présenté comme fermé, la trame part, le port revient, et la commande rend la
// main sans erreur.
func TestWOLSendEndToEnd(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolSleep = func(time.Duration) {}
	wolInterfaces = fakeNet([]string{"192.168.0.26/24"})
	var sent int
	wolDial = func(_, _ string, _ time.Duration) (net.Conn, error) {
		return &fakeConn{}, nil // la machine est allumée : rien à envoyer
	}
	wolDialUDP = func(_, _ string) (net.Conn, error) { sent++; return &fakeConn{}, nil }
	setConfig(t, "EXTERNAL=1\nEXTERNAL_URL=http://192.168.0.26:8080/v1\nEXTERNAL_MODEL=swift\nWOL=1\nWOL_MAC=b0:82:e2:46:7e:34\n")
	if err := wolSend([]string{"b0:82:e2:46:7e:34"}); err != nil {
		t.Fatalf("wolSend : %v", err)
	}
	if sent != 0 {
		t.Errorf("%d trames envoyées vers une machine allumée, attendu 0", sent)
	}
}

// TestWOLListPeersPrints : la liste des MAC du LAN, et le repli honnête quand
// aucune machine n'est vue.
func TestWOLListPeersPrints(t *testing.T) {
	testHome(t)
	defer restoreWOLHooks()
	wolRunIP = func() ([]byte, error) {
		return []byte("192.168.0.26 dev enp6s0 lladdr b0:82:e2:46:7e:34 REACHABLE\n"), nil
	}
	if err := wolListPeers(); err != nil {
		t.Fatalf("wolListPeers : %v", err)
	}
	wolRunIP = func() ([]byte, error) { return []byte("192.168.0.1 dev lo FAILED\n"), nil }
	wolRunARP = func() ([]byte, error) { return nil, errors.New("arp: introuvable") }
	if err := wolListPeers(); err != nil {
		t.Fatalf("wolListPeers (liste vide) : %v", err)
	}
}
