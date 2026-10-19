package ajean

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// backend_wol.go — Wake on LAN pour un preset externe : le PC qui sert l'API
// (llama-server, moteur MoE) est un PC du LAN, et il est parfois ÉTEINT. Un
// preset externe peut donc nommer sa MAC (clé WOL_MAC) : quand une requête part
// vers cette API et que le port refuse de répondre, AJEAN envoie le paquet
// magique, attend que le port réponde, puis REJOUE la requête. L'utilisateur
// n'a rien à renvoyer : le chat répond, une minute plus tard.
//
// Le parcours est celui du GPU Cloud Modal, dont la mécanique existe déjà
// (backend_cloud.go : attente pendant le réveil, phase affichée, /health qui ne
// réveille jamais). La seule différence est le signal de réveil : Modal répond
// 503 « Loading model » parce que le conteneur démarre ; ici le PC est éteint,
// donc il n'y a AUCUNE réponse — c'est l'absence de port qui dit « éteint », et
// c'est nous qui allumons.

const (
	wolKeyFlag = "WOL"      // "1" = ce preset peut réveiller sa cible
	wolKeyMAC  = "WOL_MAC"  // 12 chiffres hexadécimaux, les séparateurs sont ignorés
	wolKeyPort = "WOL_PORT" // port UDP de la trame : 7 (standard) ou 9 (old-school)
	wolKeyWait = "WOL_WAIT" // secondes max d'attente du port après le réveil
)

// Un nom de service systemd se valide avec extServiceRe ; une MAC se valide ici.
// Le WOL ne concerne jamais un service local : un nom `ajean-…` est un système
// d'ici, une MAC est une machine au bout du LAN. Les deux mécanismes coexistent
// dans un preset sans se marcher dessus.
var wolMACRe = regexp.MustCompile(`^[0-9A-Fa-f]{12}$`)

const (
	wolDefaultPort  = 7                 // port UDP standard du paquet magique
	wolDefaultWait  = 90 * time.Second  // attente du port, réglée par WOL_WAIT
	wolMaxWait      = 600 * time.Second // plafond dur : au-delà on ne bloque pas une génération
	wolProbeTimeout = 500 * time.Millisecond
	wolPollInterval = 2 * time.Second
	wolRounds       = 2 // certaines cartes n'écoutent la trame qu'à la deuxième rafale
	wolRoundGap     = 400 * time.Millisecond
	wolCoolDownDur  = 60 * time.Second // après un réveil sans effet : on ne repart pas en boucle
)

// wolTarget : ce qu'un preset déclare pour réveiller sa cible.
type wolTarget struct {
	MAC  string        // 12 chiffres hexadécimaux, en majuscules
	Port int           // port UDP de la trame
	Wait time.Duration // plafond d'attente du port après envoi
}

// wolOf lit la déclaration de réveil d'une config. ok=false = le preset ne veut
// rien réveiller. Un WOL=1 sans MAC valide ne réveille RIEN : c'est une config
// incomplète, pas un motif d'envoi.
func wolOf(cfg map[string]string) (wolTarget, bool) {
	if strings.TrimSpace(cfg[wolKeyFlag]) != "1" {
		return wolTarget{}, false
	}
	mac, err := wolNormalizeMAC(cfg[wolKeyMAC])
	if err != nil {
		return wolTarget{}, false
	}
	t := wolTarget{MAC: mac, Port: wolDefaultPort, Wait: wolDefaultWait}
	if p, err := strconv.Atoi(strings.TrimSpace(cfg[wolKeyPort])); err == nil && p > 0 && p <= 65535 {
		t.Port = p
	}
	if s, err := strconv.Atoi(strings.TrimSpace(cfg[wolKeyWait])); err == nil && s > 0 {
		if d := time.Duration(s) * time.Second; d > wolMaxWait {
			t.Wait = wolMaxWait // volontairement borné : une génération attend, elle ne s'arrête pas
		} else {
			t.Wait = d
		}
	}
	return t, true
}

// wolNormalizeMAC accepte b0:82:e2:46:7e:34, B0-82-E2-46-7E-34 et b082e2467e34
// (les utilitaires du LAN rendent les trois formes) et rend les 12 hex en majuscules.
func wolNormalizeMAC(raw string) (string, error) {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'F', r >= 'a' && r <= 'f':
			return r
		default:
			return -1 // : - . espace, tout séparateur : ignoré
		}
	}, strings.TrimSpace(raw))
	clean = strings.ToUpper(clean)
	if !wolMACRe.MatchString(clean) {
		return "", fmt.Errorf("MAC invalide : %q (12 chiffres hexadécimaux attendus)", strings.TrimSpace(raw))
	}
	return clean, nil
}

// wolMagicPacket construit la trame de réveil : 6 × FF puis la MAC répétée 16 fois,
// soit 102 octets (pas 128 : la trame standard est 6 + 96).
func wolMagicPacket(mac string) ([]byte, error) {
	h, err := hex.DecodeString(mac)
	if err != nil || len(h) != 6 {
		return nil, fmt.Errorf("MAC invalide : %q", mac)
	}
	b := make([]byte, 6+16*6)
	copy(b[:6], bytes.Repeat([]byte{0xFF}, 6))
	for i := 0; i < 16; i++ {
		copy(b[6+i*6:], h)
	}
	return b, nil
}

// wolBroadcastTargets calcule les destinations de la trame pour une cible — le
// point qui fait qu'un WOL marche ou non :
//   - la diffusion LIMITÉE 255.255.255.255, entendue sur tout le segment local ;
//   - la diffusion DIRIGÉE de l'interface qui porte la cible (192.168.0.255 pour
//     un /24) : beaucoup de cartes et de relais n'acceptent que celle-là ;
//   - la cible DIRECTE : marche tant que la table ARP du relais garde la MAC en
//     vie, donc souvent seulement au premier réveil suivant l'extinction.
//
// Une machine qui fait tourner AJEAN peut porter 13 interfaces (tunnels, cartes
// secondaires) et la diffusion limitée ne sort que par l'interface par défaut :
// d'où la diffusion dirigée calculée interface par interface ici.
func wolBroadcastTargets(ip string) []string {
	target := net.ParseIP(ip)
	if target == nil || target.To4() == nil {
		return []string{ip} // cible IPv6 : la diffusion limitée IPv4 ne la concerne pas
	}
	out := []string{"255.255.255.255", ip}
	ifas, err := wolInterfaces()
	if err != nil {
		return out
	}
	v4 := target.To4()
	for _, ifa := range ifas {
		if ifa.Flags&net.FlagUp == 0 || ifa.Flags&net.FlagBroadcast == 0 || ifa.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := wolAddrsOf(&ifa)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || !n.Contains(target) {
				continue
			}
			na, m := n.IP.To4(), net.IP(n.Mask).To4()
			if na == nil || m == nil {
				continue
			}
			bc := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bc[i] = na[i] | ^m[i]
			}
			if !bc.Equal(v4) {
				out = append(out, bc.String())
			}
		}
	}
	return wolDedup(out)
}

func wolDedup(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// wolGuard refuse d'envoyer un paquet magique hors du LAN. Une cible est toujours
// une IP LITTÉRALE, jamais un nom de domaine qu'on se mettrait à résoudre : un
// nom peut tomber sur une IP publique, et un paquet parti sur le 255.255.255.255
// local réveillerait n'importe qui, en boucle, sans aucun lien avec la cible.
func wolGuard(ip string) error {
	p := net.ParseIP(ip)
	if p == nil {
		return fmt.Errorf("la cible du Wake on LAN doit être une adresse IP, pas un nom : %q", ip)
	}
	if p.IsPrivate() || p.IsLoopback() || p.IsLinkLocalUnicast() {
		return nil
	}
	return fmt.Errorf("%s : le Wake on LAN est réservé au LAN (adresses privées), rien n'est envoyé", ip)
}

// wolHostPort tire l'hôte et le port de l'URL du preset externe : c'est le port
// de l'API qu'on attend après le réveil. (La clé PORT est le port du moteur LOCAL ;
// un preset externe n'en a pas, et externalPresetWithMachine la réécrit.)
func wolHostPort(rawURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", err
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("URL sans hôte : %q", rawURL)
	}
	if h, p, err := net.SplitHostPort(u.Host); err == nil {
		return h, p, nil
	}
	switch u.Scheme {
	case "https", "wss":
		return u.Hostname(), "443", nil
	default:
		return u.Hostname(), "80", nil
	}
}

// wolPorts : les ports UDP émis. Le standard est 7, l'old-school 9 ; les
// fabricants n'ont pas tous le même, et un utilitaire de WOL bien connu envoie
// sur les deux. Deux paquets de plus économisent un échec.
func wolPorts(primary int) []int {
	switch primary {
	case 7:
		return []int{7, 9}
	case 9:
		return []int{9, 7}
	default:
		return []int{primary}
	}
}

// wolDialPort sonde le port de l'API. Éteint = refus immédiat (~0,1 s mesurée) ou
// « pas de chemin » (~3 s) ; allumé et prêt = jointif. Ces deux échecs RAPIDES
// sont le signal du réveil.
func wolDialPort(host, port string) error {
	c, err := wolDial("tcp", net.JoinHostPort(host, port), wolProbeTimeout)
	if err != nil {
		return err
	}
	return c.Close()
}

// wolSendUDP émet une trame vers une destination. Go pose SO_BROADCAST lui-même
// pour une socket UDP IPv4 sur Linux, BSD et Windows (net/sockopt_linux.go:19,
// net/sockopt_windows.go:16 — AF_INET seulement, golang/go#81259) : aucun appel
// système à écrire, donc aucun fichier `…_windows.go`, et `x/sys` reste inutile.
func wolSendUDP(dst string, port int, packet []byte) error {
	c, err := wolDialUDP("udp", net.JoinHostPort(dst, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(packet)
	return err
}

// Un seul réveil à la fois par cible : trois onglets ouverts sur une machine
// éteinte ne doivent pas envoyer trois rafales. Et une cible qui n'a pas démarré
// après une rafale complète est en arrêt profond (ErP) ou a le Wake-on-LAN
// désactivé — on ne repart pas en boucle, on le dit.
var (
	wolMu       sync.Mutex
	wolWaking   = map[string]bool{}
	wolCoolDown = map[string]time.Time{}
)

// wakeWOL accomplit le parcours demandé, dans l'ordre :
//
//	sonde du port → s'il répond, rien à faire → trame(s) → attente du port → nil
//
// Renvoie nil quand le port de l'API répond (la requête peut repartir), une
// erreur quand la machine n'a pas démarré. host/port viennent de l'URL du preset.
func wakeWOL(ctx context.Context, host, port string, t wolTarget) error {
	if err := wolGuard(host); err != nil {
		return err
	}
	if wolDialPort(host, port) == nil {
		return nil // le service répond déjà : on n'envoie rien
	}
	key := host + ":" + port
	wolMu.Lock()
	if !wolWakeEnabled {
		wolMu.Unlock()
		return fmt.Errorf("%s : hors ligne, réveil désactivé ici", host)
	}
	if wolWaking[key] {
		wolMu.Unlock()
		return fmt.Errorf("réveil de %s déjà en cours", host)
	}
	if until, ok := wolCoolDown[key]; ok && time.Now().Before(until) {
		wolMu.Unlock()
		return fmt.Errorf("%s n'a pas répondu au réveil : Wake-on-LAN désactivé sur la machine, ou arrêt profond (ErP). Nouvelle tentative dans %s",
			host, time.Until(until).Truncate(time.Second))
	}
	wolWaking[key] = true
	wolMu.Unlock()
	defer func() {
		wolMu.Lock()
		delete(wolWaking, key)
		wolMu.Unlock()
	}()

	packet, err := wolMagicPacket(t.MAC)
	if err != nil {
		return err
	}
	targets := wolBroadcastTargets(host)
	ports := wolPorts(t.Port)
	start := time.Now()
	wolSetPhase("waking", host, t.MAC)
	for r := 0; r < wolRounds; r++ {
		for _, d := range targets {
			for _, p := range ports {
				if err := wolSendUDP(d, p, packet); err != nil {
					wolSetPhase("", host, "")
					return fmt.Errorf("envoi du paquet magique vers %s:%d : %w", d, p, err)
				}
			}
		}
		wolSleep(wolRoundGap)
	}

	// Attente du port : le PC démarre (POST), l'OS monte, le moteur charge son
	// modèle. Sur une machine mesurée (firmware+kernel+userspace 31 s, moteur
	// « ready » 27 s de plus), le port répond vers 70-90 s : d'où WOL_WAIT, et la
	// phase affichée pendant que la bulle de génération attend.
	deadline := start.Add(t.Wait)
	for {
		wolSleep(wolPollInterval)
		if ctx.Err() != nil { // génération annulée pendant l'attente : on arrête d'attendre
			wolSetPhase("", host, "")
			return ctx.Err()
		}
		if wolDialPort(host, port) == nil {
			wolClearCoolDown(key)
			wolSetPhase("ready", host, t.MAC)
			return nil
		}
		if time.Now().After(deadline) {
			wolSetPhase("", host, "")
			wolMu.Lock()
			wolCoolDown[key] = time.Now().Add(wolCoolDownDur)
			wolMu.Unlock()
			return fmt.Errorf("%s n'a pas redémaré (%s, %d trames vers %s en %s) : vérifier le Wake-on-LAN dans le BIOS (Power On By PCI-E, Enable PME, ErP désactivé) et le pilote (Wake on Magic Packet)",
				host, t.MAC, wolRounds*len(targets)*len(ports), strings.Join(targets, " "), t.Wait)
		}
		wolSetPhase("waiting", host, fmt.Sprintf("%s · %s écoulées", t.MAC, time.Since(start).Truncate(time.Second)))
	}
}

func wolClearCoolDown(key string) {
	wolMu.Lock()
	delete(wolCoolDown, key)
	wolMu.Unlock()
}

// wolSetPhase : étape de réveil visible pendant l'attente, dans la bulle de
// génération. portWOLPhase est posé par web_api.go (phase « wol » à côté de la
// phase cloud) ; nil = pas d'affichage (CLI, tests).
var wolSetPhase = func(phase, host, detail string) {}

// wolPhase : étape du réveil en cours, pour la bulle de génération — la même
// mécanique que cloudPhase/cloud_phase côté Modal.
type wolPhaseState struct {
	sync.Mutex
	phase  string
	host   string
	detail string
	at     time.Time
}

var wolPhaseLive wolPhaseState

// wolPhaseActive : étape de réveil du preset actif, en {phase, hôte, détail} ;
// phase vide = aucun réveil en cours (et rien d'affiché). Une étape ancienne de
// plus de wolMaxWait est abandonnée : un réveil qui a rendu la main par un chemin
// inattendu ne doit pas laisser l'interface afficher « réveil en cours » indéfiniment.
func wolPhaseActive() (phase, host, detail string) {
	wolPhaseLive.Lock()
	defer wolPhaseLive.Unlock()
	if wolPhaseLive.phase == "" || time.Since(wolPhaseLive.at) > wolMaxWait {
		return "", "", ""
	}
	return wolPhaseLive.phase, wolPhaseLive.host, wolPhaseLive.detail
}

// wolPhaseClear efface l'étape affichée (fin de réveil, succès ou échec).
func wolPhaseClear() {
	wolPhaseLive.Lock()
	wolPhaseLive.phase = ""
	wolPhaseLive.host = ""
	wolPhaseLive.detail = ""
	wolPhaseLive.Unlock()
}

// wolPhaseSet : l'affichage réel de l'étape de réveil. Les tests posent leur
// propre wolSetPhase pour observer les étapes (voir TestWOLPhaseAffichee) et
// restoreWOLHooks rend celle-ci.
func wolPhaseSet(phase, host, detail string) {
	wolPhaseLive.Lock()
	wolPhaseLive.phase, wolPhaseLive.host, wolPhaseLive.detail, wolPhaseLive.at = phase, host, detail, time.Now()
	wolPhaseLive.Unlock()
}

func init() { wolSetPhase = wolPhaseSet }

// wolProbeOnly : un simple sondage de santé ne réveille JAMAIS. C'est la règle
// déjà posée pour le GPU Cloud (cloud_proxy.go : /health ne réveille pas le GPU,
// et son test le prouve) ; elle est reprise ici pour que l'interface, les
// sondeurs et `ajean test` n'aient aucun effet de bord sur un PC éteint.
func wolProbeOnly() bool { return !wolWakeEnabled }

// Les points d'appel réseau sont des variables pour que les tests n'aient à
// toucher ni au réseau réel, ni aux vraies cartes réseau — le style du repo
// (extUnitAction, cloudDeployOpt, firewallApply).
var (
	wolDial        = net.DialTimeout
	wolDialUDP     = net.Dial
	wolInterfaces  = net.Interfaces
	wolAddrsOf     = (*net.Interface).Addrs // adresses d'une interface, remplaçables par les tests
	wolSleep       = time.Sleep
	wolWakeEnabled = true
)

// wolLLM : la requête part vers une API dont le PC est éteint. Le parcours est
// celui du GPU Cloud (la requête est REJOUÉE, le corps est gardé en mémoire),
// avec le signal de réveil à la place : ici le PC éteint ne répond pas du tout,
// donc c'est l'absence du port qui dit « éteint », et nous qui allumons.
//
// Une seule tentative de réveil par appel. Si la machine ne démarre pas,
// l'erreur remonte avec ce qu'il faut vérifier : elle n'est pas masquée par un
// « le moteur ne répond pas » qui ferait chercher un moteur là où il n'y a pas
// de moteur. Et si la machine est allumée, wakeWOL rend la main sans rien
// envoyer : le premier appel de complétion part directement.
func wolLLM(ctx context.Context, req *http.Request, body []byte, ep chatEndpoint) (*http.Response, error) {
	host, port, err := wolHostPort(ep.URL)
	if err != nil {
		return http.DefaultClient.Do(req) // URL que le WOL ne sait pas lire : chemin normal
	}
	if err := wakeWOL(ctx, host, port, ep.WOL); err != nil {
		return nil, wolFriendlyError(err, host, ep)
	}
	return http.DefaultClient.Do(req)
}

// wolLookupHost : résoudre un nom en adresses, en variable pour que les tests
// ne touchent pas le DNS.
var wolLookupHost = net.LookupHost

// wolMACForHost : la MAC de la machine qui RÉPOND à cette adresse, lue dans la
// table ARP. Un test de connexion réussi vient de forcer la résolution ARP de la
// cible : la table contient donc la machine, et c'est là qu'on peut apprendre sa
// MAC — la machine est allumée, c'est le moment de la stocker.
//
// Conditions, et pourquoi :
//   - IP privée du LAN uniquement. Une adresse publique port-forcée a une entrée
//     ARP qui est celle du ROUTEUR : la recopier comme cible de réveil réveillerait
//     la passerelle, pas le PC. Rien n'est proposé.
//   - pas de boucle locale : cette machine est celle d'AJEAN, elle est allumée.
//   - la table ARP ne contient que ce qui a été joint récemment ; une cible hors
//     de la liaison (routée) n'y est pas. Une absence se dit, elle ne s'invente pas.
func wolMACForHost(rawHost string) (string, error) {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return "", fmt.Errorf("aucune cible à lire")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := wolLookupHost(host)
		if err != nil || len(addrs) == 0 {
			return "", fmt.Errorf("nom introuvable : %s", host)
		}
		ip = net.ParseIP(addrs[0])
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", fmt.Errorf("%s : pas une IPv4, rien à lire dans la table ARP", host)
	}
	// L'ordre compte : IsPrivate() vaut false pour 127.0.0.1 (mesuré), donc la
	// boucle locale et les adresses de CETTE machine se testent avant le LAN,
	// sinon elles ressortiraient « hors du LAN ».
	if v4.IsLoopback() || wolIsLocalIP(v4) {
		return "", fmt.Errorf("%s : c'est cette machine, elle est déjà allumée", host)
	}
	if !v4.IsPrivate() {
		return "", fmt.Errorf("%s : hors du LAN, la table ARP donnerait le routeur, pas la machine", host)
	}
	peers, err := wolARPTable()
	if err != nil {
		return "", err
	}
	want := v4.String()
	for _, p := range peers {
		if p.ip == want {
			return p.mac, nil
		}
	}
	return "", fmt.Errorf("%s n'est pas dans la table ARP (la table ne garde que ce qui a été joint récemment)", want)
}

// Un réveil proposé par la sonde de la modale, jamais envoyé par elle : la
// classification distingue la machine injoignable de la machine qui répond sans
// service, parce que le paquet magique n'aide que dans le premier cas.
const (
	wolHintNone = iota // rien à voir avec un réveil : message d'origine
	wolHintWake        // injoignable : la machine est peut-être éteinte
	wolHintPort        // la machine répond, le service manque : WOL n'y change rien
)

// wolWakeHint classe une panne de connexion vers une cible de preset externe.
// Classifieur pur : aucune requête réseau, aucun envoi, il ne lit que l'erreur.
//
//	wolHintWake : « no route to host », « network is unreachable », délai dépassé.
//	          Rien ne répond à cette IP : la machine est hors ligne (éteinte,
//	          arrêt profond, ou hors du LAN). Un paquet magique peut la sortir de là.
//	wolHintPort : « connection refused ». Le refus vient d'un RST de la MACHINE :
//	          elle est allumée, c'est le service qui manque. Envoyer un paquet
//	          magique serait du bruit — il faut démarrer l'API, pas réveiller le PC.
//	wolHintNone : le reste (TLS, HTTP, DNS, URL). Un échec TLS prouve qu'un service
//	          écoute : la machine est allumée, le WOL n'a rien à faire ici.
//
// Le réveil coché et une MAC saisie sont requis : sans eux, rien n'est proposé.
func wolWakeHint(err error, host string, wolOn bool, mac string) int {
	if err == nil || !wolOn {
		return wolHintNone
	}
	if _, err := wolNormalizeMAC(mac); err != nil || mac == "" {
		return wolHintNone
	}
	if wolGuard(host) != nil {
		return wolHintNone // pas une IP privée du LAN : pas de trame possible
	}
	var operr *net.OpError
	if !errors.As(err, &operr) {
		return wolHintNone
	}
	switch {
	case errors.Is(operr, syscall.ECONNREFUSED):
		return wolHintPort
	case errors.Is(operr, syscall.EHOSTUNREACH), errors.Is(operr, syscall.ENETUNREACH):
		return wolHintWake
	}
	var neterr net.Error
	if errors.As(err, &neterr) && neterr.Timeout() {
		return wolHintWake
	}
	return wolHintNone
}

// wolFriendlyError : le texte vu dans la bulle de chat. La ligne utile est celle
// qui dit quoi faire sur la machine réveillée — le WOL est un réglage de BIOS et
// de pilote, pas un réglage d'AJEAN.
func wolFriendlyError(err error, host string, ep chatEndpoint) error {
	return fmt.Errorf("⚠️ %s est éteint et ne s'est pas réveillé (%s) : %v", host, ep.WOL.MAC, err)
}

// wolActive et wolActiveDetail : l'étape de réveil du preset ACTIF, pour
// /api/status — la parité de cloudPhaseActive. Un preset qui ne déclare pas de
// MAC n'a rien à afficher, même si un réveil d'un autre preset est en vol :
// l'interface ne doit pas raconter un réveil que le preset affiché ne fait pas.
func wolActive() string {
	if t, _ := wolOf(ReadConfig()); t.MAC == "" {
		return ""
	}
	p, _, _ := wolPhaseActive()
	return p
}

func wolActiveDetail() string {
	if t, _ := wolOf(ReadConfig()); t.MAC == "" {
		return ""
	}
	_, _, d := wolPhaseActive()
	return d
}
