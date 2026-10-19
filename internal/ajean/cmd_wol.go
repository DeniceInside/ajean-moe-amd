package ajean

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// cmd_wol.go — `ajean wol` : réveiller une machine du LAN par paquet magique.
//
//	ajean wol                      la déclaration du preset actif, et ce qui manque
//	ajean wol <MAC>                réveiller cette machine (la cible : hôte du preset actif)
//	ajean wol <MAC> -ip 10.0.7.9   réveiller une autre machine du LAN
//	ajean wol list                 les MAC visibles sur le LAN, à recopier dans le preset
//
// Le WOL est un réglage de la MACHINE RÉVEILLÉE (BIOS + pilote), pas d'AJEAN :
// c'est pourquoi la commande lit ce qu'elle peut et rend la commande exacte à
// faire là-bas, au lieu de faire semblant. La sonde de santé, elle, ne réveille
// jamais (wolActive n'affiche qu'un réveil déjà en vol).

func cmdWOL(args []string) error {
	if len(args) > 0 && args[0] == "list" {
		return wolListPeers()
	}
	if len(args) > 0 && args[0] == "check" {
		args = args[1:]
		return wolCheck(args)
	}
	return wolSend(args)
}

// wolSend : le parcours complet, avec ce qui se passe à chaque étape.
func wolSend(args []string) error {
	macArg := ""
	ip, port := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-ip", "--ip":
			if i+1 < len(args) {
				ip = args[i+1]
				i++
			}
		case "-port", "--port":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		default:
			macArg = args[i]
		}
	}
	cfg := ReadConfig()
	tt, declared := wolOf(cfg)
	mac := macArg
	if mac == "" {
		if !declared {
			return fmt.Errorf("aucune MAC à réveiller : ce preset n'a pas de Wake on LAN (clé WOL_MAC de la modale API Externe), et aucun MAC n'est donné\n  usage : ajean wol aa:bb:cc:dd:ee:ff [-ip 192.168.0.26] [-port 8080]")
		}
		mac = tt.MAC
	}
	tt2, err := wolTargetFrom(mac, tt)
	if err != nil {
		return err
	}
	host, svcPort, err := wolTargetHost(ip, port, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("%s réveil de %s (%s, API sur %s:%s)\n", cyan("Wake on LAN"), host, tt2.MAC, host, svcPort)
	start := time.Now()
	if err := wakeWOL(context.Background(), host, svcPort, tt2); err != nil {
		return err
	}
	fmt.Printf("%s API joignable en %s\n", green("[ok]"), time.Since(start).Truncate(time.Second))
	return nil
}

// wolTargetFrom normalise le MAC demandé et reprend le port/attente déclarés.
func wolTargetFrom(mac string, base wolTarget) (wolTarget, error) {
	m, err := wolNormalizeMAC(mac)
	if err != nil {
		return wolTarget{}, err
	}
	base.MAC = m
	return base, nil
}

// wolTargetHost : la machine à réveiller et le port de son API. Par défaut,
// l'hôte de l'URL du preset actif. -ip permet de réveiller une AUTRE machine du
// LAN (un portable qu'on vient d'éteindre) avec le même port d'API.
func wolTargetHost(ip, port string, cfg map[string]string) (string, string, error) {
	h, p, err := wolHostPort(strings.TrimSpace(cfg[extKeyURL]))
	if err != nil {
		h, p = "", "8080"
	}
	host := strings.TrimSpace(ip)
	if host == "" {
		if h == "" {
			return "", "", fmt.Errorf("aucune cible : le preset actif n'a pas d'URL externe, et -ip n'est pas donné\n  usage : ajean wol aa:bb:cc:dd:ee:ff -ip 192.168.0.26")
		}
		host = h
	}
	if v := strings.TrimSpace(port); v != "" {
		return host, v, nil
	}
	return host, p, nil
}

// wolListPeers : les MAC visibles sur le LAN, avec l'IP qui va avec. C'est la
// réponse à « où je trouve cette MAC ? » — la table ARP de la machine qui fait
// tourner AJEAN contient les machines qui lui ont parlé, ce qui est exactement la
// liste de celles qui peuvent être réveillées par diffusion dirigée.
func wolListPeers() error {
	neigh, err := wolARPTable()
	if err != nil {
		return fmt.Errorf("table ARP illisible : %v", err)
	}
	if len(neigh) == 0 {
		fmt.Printf("%s aucune machine vue sur le LAN\n", yellow("[attention]"))
		fmt.Printf("%s la table ARP ne contient que les machines qui vous ont parlé ; allumez la machine\n"+
			"  ciblée une fois, ou lisez sa MAC dessus : %s (Windows), %s (Linux)\n",
			dim("[info]"), bold("getmac /v"), bold("ip -o link show | grep ether"))
		return nil
	}
	fmt.Printf("%s machines vue(s) sur le LAN — la MAC à recopier dans le preset :\n", cyan("Wake on LAN"))
	for _, n := range neigh {
		fmt.Printf("  %-16s %s\n", bold(n.ip), n.mac)
	}
	fmt.Printf("%s le Wake-on-LAN doit être activé sur la machine ciblée (BIOS + pilote) : %s\n",
		dim("[info]"), bold("ajean wol check <ip>"))
	return nil
}

type wolPeer struct{ ip, mac string }

// wolARPTable lit la table des voisins. `ip -4 neigh` est l'outil standard de
// Linux ; sur Windows, `arp -a` est celui qui existe partout. Les deux sont lus,
// pas écrits. Un système sans ces outils rend une erreur, pas une liste vide.
// Les appels sont des variables (wolRunIP, wolRunARP, wolRunEthtool) pour que les
// tests n'exécutent aucun binaire système.
func wolARPTable() ([]wolPeer, error) {
	out, err := wolRunIP()
	if err == nil {
		var peers []wolPeer
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			ip := net.ParseIP(f[0])
			if !wolListableIP(ip) {
				continue
			}
			for _, tok := range f {
				if m, err := wolNormalizeMAC(tok); err == nil && len(tok) == 17 {
					peers = append(peers, wolPeer{ip: f[0], mac: m})
					break
				}
			}
		}
		return peers, nil
	}
	out2, err2 := wolRunARP()
	if err2 != nil {
		return nil, fmt.Errorf("ni `ip -4 neigh` ni `arp -a` n'ont répondu (%v / %v)", err, err2)
	}
	var peers []wolPeer
	for _, line := range strings.Split(string(out2), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(strings.Trim(f[0], "()"))
		if !wolListableIP(ip) {
			continue
		}
		for _, tok := range f[1:] {
			if m, err := wolNormalizeMAC(tok); err == nil && len(tok) == 17 {
				peers = append(peers, wolPeer{ip: ip.String(), mac: m})
				break
			}
		}
	}
	return peers, nil
}

// wolListableIP : une adresse qui a du sens dans la liste « à recopier dans un
// preset ». Une adresse de LAN, en UNICAST : ni multicast (224.x/239.x, que
// `arp -a` affiche avec une MAC de groupe sans machine derrière — la recopier
// comme cible de réveil serait une MAC qu'on ne peut pas écrire dans le BIOS),
// ni publique (le WOL ne sort pas du LAN), ni liée.
func wolListableIP(ip net.IP) bool {
	return ip != nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// wolCheck : ce qui doit être réglé sur la machine ciblée. AJEAN ne pilote PAS
// le BIOS ni le pilote d'une machine distante — il dit ce qu'il faut y vérifier,
// avec la commande exacte à y taper. La lecture locale (Wake-on du driver, via
// ethtool) n'est possible que si c'est CETTE machine qui est ciblée.
func wolCheck(args []string) error {
	ip := ""
	if len(args) > 0 {
		ip = strings.TrimSpace(args[0])
	}
	cfg := ReadConfig()
	tt, declared := wolOf(cfg)
	if ip == "" {
		h, _, err := wolHostPort(strings.TrimSpace(cfg[extKeyURL]))
		if err != nil || h == "" {
			return fmt.Errorf("aucune machine à vérifier : le preset actif n'a pas d'URL externe\n  usage : ajean wol check 192.168.0.26")
		}
		ip = h
	}
	if err := wolGuard(ip); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", cyan("Wake on LAN — vérification"), bold(ip))
	if declared {
		fmt.Printf("  MAC déclarée par le preset : %s\n", bold(tt.MAC))
	} else {
		fmt.Printf("  %s (clé WOL_MAC de la modale API Externe)\n", yellow("le preset actif ne déclare pas de MAC"))
	}
	if p := net.ParseIP(ip); p != nil && wolIsLocalIP(p) {
		if on, err := wolLocalWakeSetting(); err == nil {
			if on {
				fmt.Printf("  cette machine : %s (Wake-on du pilote activé)\n", green("réveil armé"))
			} else {
				fmt.Printf("  cette machine : %s — réglage local, exige root : %s\n",
					yellow("pilote non armé pour le réveil"), bold("sudo ethtool -s <interface> wol g"))
			}
		} else {
			fmt.Printf("  pilote : %s (%v)\n", dim("non lisible ici"), err)
		}
	}
	fmt.Printf("\n%s sur %s, les trois couches à activer (le WOL échoue presque toujours ici) :\n",
		dim("[info]"), ip)
	fmt.Printf("  1. BIOS/UEFI : Wake on LAN / Power On By PCI-E = %s, Enable PME = %s, %s = %s\n",
		bold("Enabled"), bold("Enabled"), bold("ErP Ready / Deep Sleep"), bold("Disabled"))
	if runtime.GOOS == "windows" {
		fmt.Printf("  2. Pilote : %s = %s, et %s = %s\n",
			bold("Wake on Magic Packet"), bold("Enabled"), bold("Allow the computer to turn off this device"), bold("Unchecked"))
		fmt.Printf("    lecture : %s\n", dim("powershell -NoProfile -NonInteractive -Command \"Get-NetAdapterPowerManagement | fl *Wake*\""))
		fmt.Printf("  3. Windows : %s = %s (sinon l'arrêt hybride n'alimente pas la carte en S5)\n",
			bold("Fast Startup / HiberbootEnabled"), bold("0"))
		fmt.Printf("    lecture : %s\n", dim("powercfg /a"))
	} else {
		fmt.Printf("  2. Pilote : %s (lecture : %s)\n", bold("Wake on Magic Packet"), dim("ethtool <interface> | grep -i wake-on"))
		fmt.Printf("  3. État d'arrêt : le réveil doit être armé en %s (arrêt complet), pas en veille moderne sans réseau alimenté\n", bold("S5"))
		fmt.Printf("    lecture : %s\n", dim("cat /proc/acpi/wakeup  (la carte doit y figurer, *enabled)"))
	}
	if declared {
		fmt.Printf("\n%s test : %s\n", dim("[info]"), bold("ajean wol "+tt.MAC+" -ip "+ip))
	} else {
		fmt.Printf("\n%s test, la MAC en main : %s\n", dim("[info]"), bold("ajean wol aa:bb:cc:dd:ee:ff -ip "+ip))
	}
	return nil
}

// Les outils système lus par `ajean wol` (lecture seule, jamais d'écriture) :
// variables pour que les tests n'exécutent ni ip, ni arp, ni ethtool.
var (
	wolRunIP      = func() ([]byte, error) { return exec.Command("ip", "-4", "neigh", "show").CombinedOutput() }
	wolRunARP     = func() ([]byte, error) { return exec.Command("arp", "-a").CombinedOutput() }
	wolRunEthtool = func(iface string) ([]byte, error) { return exec.Command("ethtool", iface).CombinedOutput() }
)

// wolIsLocalIP : la cible est-elle une adresse de CETTE machine ? (le réglage du
// pilote ne se lit que sur la machine qui porte la carte).
func wolIsLocalIP(p net.IP) bool {
	ifas, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifa := range ifas {
		addrs, err := ifa.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(p) {
				return true
			}
		}
	}
	return false
}

// wolLocalWakeSetting lit le réglage de réveil du pilote local (Linux : `ethtool`,
// qui exige root ; macOS/Windows : pas de lecture portable, on rend une erreur et
// wolCheck affiche la commande à faire à la main). LECTURE SEULE, toujours.
func wolLocalWakeSetting() (bool, error) {
	if runtime.GOOS != "linux" {
		return false, fmt.Errorf("lecture du pilote non portable sur %s", runtime.GOOS)
	}
	iface, err := wolDefaultInterface()
	if err != nil {
		return false, err
	}
	out, err := wolRunEthtool(iface)
	if err != nil {
		return false, fmt.Errorf("%s (lecture du pilote : %s)", strings.TrimSpace(string(out)), err)
	}
	return wolParseWakeOn(out)
}

// wolParseWakeOn lit le réglage de réveil dans une sortie d'ethtool. Analyse pure,
// sans dépendre de la plateforme ni d'un binaire : c'est elle qui est testée, et
// wolLocalWakeSetting n'est que la porte d'accès (Linux, ethtool, root).
func wolParseWakeOn(out []byte) (bool, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "wake-on") {
			continue
		}
		// "Wake-on: g" = armé pour le paquet magique ; "Wake-on: d" = désarmé.
		low := strings.ToLower(line)
		after := strings.TrimSpace(line[strings.Index(low, "wake-on")+len("wake-on"):])
		after = strings.TrimSpace(strings.TrimPrefix(after, ":"))
		// Les drapeaux sont un ENSEMBLE DE LETTRES COLLÉES, pas des mots : « d »
		// désarmé, « g » paquet magique, « u » unicast, « b » broadcast, « a » ARP.
		// La sortie d'un pilote qui a plusieurs réveils armés est « Wake-on: dgu ».
		// C'est pour ça qu'on cherche la lettre g dans l'ensemble, pas un mot égal.
		flags := strings.Join(strings.Fields(strings.Trim(after, "()")), "")
		return strings.ContainsAny(flags, "g"), nil
	}
	return false, fmt.Errorf("réglage de réveil absent de la sortie d'ethtool")
}

func wolDefaultInterface() (string, error) {
	ifas, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifa := range ifas {
		if ifa.Flags&net.FlagLoopback != 0 || ifa.Flags&net.FlagUp == 0 {
			continue
		}
		if strings.HasPrefix(ifa.Name, "docker") || strings.HasPrefix(ifa.Name, "veth") ||
			strings.HasPrefix(ifa.Name, "virbr") || strings.HasPrefix(ifa.Name, "br-") {
			continue
		}
		addrs, err := ifa.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if v4 := n.IP.To4(); v4 != nil && v4.IsPrivate() {
					return ifa.Name, nil
				}
			}
		}
	}
	return "", fmt.Errorf("aucune interface réseau privée trouvée")
}
