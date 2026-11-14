//go:build linux

package ajean

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// L'environnement ROCm d'une unité : ce que le moteur compilé en HIP et la
// télémétrie AMD exigent, et qu'un service ne reçoit pas tout seul.
func TestRocmServiceEnv(t *testing.T) {
	root := mkRocm(t, true)

	// Machine sans ROCm : rien dans l'unité. Une machine NVIDIA/Intel/Apple
	// n'a pas à voir traîner de Environment=ROCm.
	if got := rocmServiceEnv(""); got != "" {
		t.Errorf("racine absente : %q, attendu \"\"", got)
	}

	env := rocmServiceEnv(root)
	if !strings.Contains(env, "Environment=PATH="+filepath.Join(root, "bin")+":") {
		t.Errorf("PATH ne démarre pas par <root>/bin :\n%s", env)
	}
	if !strings.Contains(env, ":") {
		t.Error("PATH sans séparateur")
	}
	// Le PATH par défaut de systemd est CONSERVÉ : un Environment=PATH remplace
	// la valeur héritée. Un ajean qui ne trouve plus cmake/git/node depuis son
	// service est plus cassé qu'avant.
	pathLine := ""
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(line, "Environment=PATH=") {
			pathLine = line
		}
	}
	for _, d := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin"} {
		if !strings.Contains(pathLine, ":"+d+":") && !strings.HasSuffix(pathLine, ":"+d) {
			t.Errorf("PATH système perdu (%q) : %q", d, pathLine)
		}
	}
	if !strings.Contains(env, "Environment=ROCM_PATH="+root) {
		t.Error("ROCM_PATH absente (variable lue par ggml-hip pour find_package(hip))")
	}
	if !strings.Contains(env, "Environment=LD_LIBRARY_PATH="+filepath.Join(root, "lib")) {
		t.Error("LD_LIBRARY_PATH absente (libamdhip64/librocblas à la charge du moteur)")
	}
	if strings.Contains(env, "\r") {
		t.Error("retours chariot dans un fichier d'unité")
	}
}

// Le bloc généré doit se lire comme un fragment d'unité : chaque ligne est un
// couple clé=valeur du [Service] (sinon systemd ignore la ligne en silence, avec
// un avertissement dans le journal).
func TestRocmServiceEnvLisibleParSystemd(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(rocmServiceEnv(mkRocm(t, true))))
	n := 0
	for sc.Scan() {
		line := sc.Text()
		n++
		if line == "" {
			t.Fatalf("ligne vide dans le bloc (unité n°%d)", n)
		}
		if !strings.HasPrefix(line, "Environment=") {
			t.Fatalf("ligne %d hors Environment= : %q", n, line)
		}
		if strings.Contains(strings.SplitN(line, "=", 3)[2], " ") {
			t.Fatalf("espace non protégée dans Environment= : %q (systemd découpe sur les espaces)", line)
		}
	}
	if n != 3 {
		t.Errorf("%d lignes, attendu 3 (PATH, ROCM_PATH, LD_LIBRARY_PATH)", n)
	}
}

// Les deux unités doivent porter l'environnement : c'est « ajean llamacpp
// install » lancé depuis le service (compilation) et l'UI (télémétrie) qui en
// ont besoin.
func TestUnitesPortentEnvironnementRocm(t *testing.T) {
	root := mkRocm(t, true)
	env := rocmServiceEnv(root)
	svc, exe := serviceName(), installedExePath()

	unit := fmt.Sprintf(engineUnitTemplate, "ajean-test-user", "/etc/ajean", exe+" serve", env)
	for _, want := range []string{"[Service]", "Environment=PATH=" + filepath.Join(root, "bin"),
		"Environment=ROCM_PATH=" + root, "ExecStart=" + exe + " serve", "Restart=on-failure"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unité moteur : %q absent\n---\n%s", want, unit)
		}
	}

	ui := fmt.Sprintf(uiUnitTemplate, svc, "ajean-test-user", "/etc/ajean", exe+" web", env)
	for _, want := range []string{"Environment=ROCM_PATH=" + root, "Restart=always",
		"After=network-online.target " + svc + ".service"} {
		if !strings.Contains(ui, want) {
			t.Errorf("unité UI : %q absent\n---\n%s", want, ui)
		}
	}

	// Un Environment= avalé dans la ligne d'un autre champ (champ manquant dans le
	// format) casse silencieusement la directive suivante : Restart= doit rester
	// seul sur sa ligne, et aucune ligne ne doit porter deux directives.
	for _, body := range []string{unit, ui} {
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "Environment=") && strings.Contains(line, "Restart=") {
				t.Errorf("Environment= collé à Restart= : %q", line)
			}
			if strings.Contains(line, "%[") || strings.Contains(line, "%s") {
				t.Errorf("champ non rempli dans l'unité : %q", line)
			}
		}
	}

	// Machine sans ROCm : les unités gardent leur forme d'origine, sans bloc.
	plain := fmt.Sprintf(engineUnitTemplate, "u", "/etc/ajean", "/bin/ajean serve", "")
	if strings.Contains(plain, "Environment=") {
		t.Error("Environment= posé sur une machine sans ROCm")
	}
	if !strings.Contains(plain, "Restart=on-failure") {
		t.Error("Restart= abîmé quand le bloc est vide")
	}
}
