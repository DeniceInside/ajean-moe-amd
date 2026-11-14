package ajean

import (
	"encoding/json"
	"strings"
	"testing"
)

// Le nom porté par l'interface : le fork, pas l'amont.
//
// Ce nom est ce que l'utilisateur voit — titre de la fenêtre du lanceur
// Chromium (--app, service ajean-ui), nom de l'app ajoutée à l'écran d'accueil
// sur iPhone, nom dans le gestionnaire de tâches, titre des notifications Web
// Push. Deux binaires peuvent cohabiter sur la même machine, sur le même port
// 8090 : sans nom distinctif, on ne sait pas lequel des deux tourne.
//
// index.html est GÉNÉRÉ depuis ui/src/ (tools/assemble-ui, go:generate). Le test
// lit le fichier embarqué par go:embed — celui qui est réellement servi — et
// vérifie donc la chaîne livrée, pas la chaîne du template. Il échoue aussi si
// l'index.html committé décroche de sa source (oubli de `go generate`).
func TestInterfaceNommeLeFork(t *testing.T) {
	const want = "AJEAN-MOE-AMD"

	idx, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(idx)
	if !strings.Contains(page, "<title>"+want+"</title>") {
		t.Errorf("index.html embarqué : <title>%s</title> absent — la fenêtre du lanceur "+
			"porte le nom du binaire installé, c'est le seul repère visuel entre le fork "+
			"et l'amont (même port 8090)", want)
	}
	if !strings.Contains(page, `apple-mobile-web-app-title" content="`+want+`"`) {
		t.Error("apple-mobile-web-app-title : le nom de l'app iOS n'est pas le fork")
	}

	// Manifeste PWA : nom affiché par Android/desktop à l'installation.
	raw, err := uiFS.ReadFile("ui/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest.webmanifest illisible : %v", err)
	}
	if man.Name != want || man.ShortName != want {
		t.Errorf("manifest : name=%q short_name=%q, attendu %q aux deux", man.Name, man.ShortName, want)
	}

	// Service worker : le titre de la notification « réponse prête », émise app
	// fermée — c'est là que le nom se lit sans l'écran de l'UI.
	sw, err := uiFS.ReadFile("ui/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sw), "title: '"+want+"'") {
		t.Error("sw.js : le titre de la notification Web Push n'est pas le fork")
	}

	// Le mot « AJEAN » reste la marque (logo, textes, ajean.link) : ce test ne
	// demande pas l'absence de l'amont, seulement que les supports NOMMÉS soient
	// ceux du fork. Un fork qui s'appellerait encore AJEAN se prend pour l'amont.
	if strings.Count(page, "AJEAN") < 5 {
		t.Error("la marque AJEAN a disparu de l'interface : le fork est un fork de AJEAN, il doit le dire")
	}
}
