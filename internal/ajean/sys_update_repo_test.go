package ajean

import (
	"strings"
	"testing"
)

// La SOURCE DE MISE À JOUR d'un build fork est le fork, pas l'amont.
//
// Pourquoi ce test : `ajean update` compare la version du binaire à la release la
// plus récente du dépôt pointé par updateRepo, puis installe cet asset. Pointé
// sur le dépôt amont, un binaire de fork se déclarait « déjà à jour » (comparé à
// l'amont) puis, à la première version amont supérieure, remplaçait le binaire du
// fork — support AMD, MoE, détection KFD, Wake on LAN, chemins ROCm — par le
// binaire amont, qui ne les a pas. Le fork perdait sa raison d'être sans que
// personne ne lance la commande : c'est le défaut n°3 relevé sur la release v0.18.2
// (mesuré ce jour-là : « déjà à jour (0.18.1) » sur le binaire du fork).
//
// Le test ne vérifie pas une valeur décorative : il interdit le retour à l'amont,
// qui est précisément ce qu'un reprise de commit amont produirait.
func TestSourceDeMAJViseLeFork(t *testing.T) {
	if updateRepo != "DeniceInside/ajean-moe-amd" {
		t.Errorf("updateRepo = %q, attendu le fork DeniceInside/ajean-moe-amd — "+
			"un build de fork qui consulte l'amont finit par installer un binaire "+
			"sans support AMD/MoE", updateRepo)
	}
	if strings.Contains(updateRepo, "nathaninline") {
		t.Error("updateRepo pointe le dépôt amont : la mise à jour effacerait le fork")
	}
}

// Le moteur MoE, lui, vient de l'amont : le fork ne publie aucun tag moe-v*.
// Ce test garde la distinction — les deux sources ne suivent pas le même dépôt,
// et confondre les deux casse l'installation du moteur (404 sur l'asset).
func TestMoteurMoEVientDeLAmont(t *testing.T) {
	if !strings.Contains(moeReleaseBase, "nathaninline/ajean") {
		t.Errorf("moeReleaseBase = %q — le paquet du moteur est publié par l'amont "+
			"(tag moe-v%s) : le fork n'a pas cet asset, l'installation du moteur "+
			"échouerait", moeReleaseBase, moeVersion)
	}
	if strings.Contains(moeReleaseBase, "DeniceInside") {
		t.Error("moeReleaseBase pointe le fork : aucun tag moe-v* n'y est publié, l'installation du moteur renverrait 404")
	}
}

// Les noms d'assets du fork sont ceux de l'amont (release.yml) : la logique de
// choix d'asset ne change pas quand on change de dépôt. Vérifié sur les releases
// réelles du fork et de l'amont : mêmes noms, mêmes 9 assets.
func TestForkPublieLesNomsDAssetAttendus(t *testing.T) {
	for _, c := range []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
	} {
		name := assetNameFor(c.goos, c.goarch)
		if !strings.HasPrefix(name, "ajean-") {
			t.Errorf("%s/%s → %q : la release du fork ne publie que des assets « ajean- » (SHA256SUMS est calculé par ce filtre)", c.goos, c.goarch, name)
		}
	}
}
