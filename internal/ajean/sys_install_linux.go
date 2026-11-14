//go:build linux

package ajean

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sys_install_linux.go — la part systemd de l'installation. Le parcours commun
// (dossiers, lien du binaire, /etc/default, chown) vit dans sys_install_unix.go.

// sudoersTemplate autorise l'utilisateur à piloter UNE unité sans mot de passe.
// Posé pour les deux : l'interface, qui tourne sans privilèges, doit pouvoir
// redémarrer le moteur (bascule de preset) comme elle-même (nouveau jeton).
const sudoersTemplate = `# Permet à %[1]s de piloter l'unité %[2]s sans mot de passe (posé par ajean install).
%[1]s ALL=(root) NOPASSWD: /bin/systemctl start %[2]s, /bin/systemctl stop %[2]s, /bin/systemctl restart %[2]s, /bin/systemctl enable %[2]s, /bin/systemctl disable %[2]s
`

// engineUnitTemplate — le moteur : exec llama-server, supervisé directement par
// systemd. Champs : User, WorkingDirectory, ExecStart.
const engineUnitTemplate = `[Unit]
Description=AJEAN-MOE-AMD — moteur llama.cpp (fork AMD/MoE)
After=network.target

[Service]
Type=simple
User=%[1]s
WorkingDirectory=%[2]s
ExecStart=%[3]s
%[4]sRestart=on-failure
RestartSec=3
# llama-server ignore SIGTERM tant qu'une génération est en cours : avec le
# défaut systemd (90 s), chaque bascule de preset en plein flux restait bloquée
# une minute et demie. Il n'a rien à sauvegarder, on le coupe au bout de 5 s.
TimeoutStopSec=5

# Priorité CPU : on remonte le process pour qu'il ne soit pas dépriorisé face
# aux tâches de fond (sampling/orchestration côté CPU pèsent sur le débit même
# en inference GPU). Nice négatif + scheduling normal réactif.
Nice=-10
CPUSchedulingPolicy=other

[Install]
WantedBy=multi-user.target
`

// uiUnitTemplate — l'interface : UI web locale, tunnel du relais et endpoint
// OpenAI, servis par un SEUL process (donc une seule conversation).
// Champs : unité du moteur (dépendance), User, WorkingDirectory, ExecStart.
const uiUnitTemplate = `[Unit]
Description=AJEAN-MOE-AMD — interface web + accès distant (fork AMD/MoE)
After=network-online.target %s.service
Wants=network-online.target

[Service]
Type=simple
User=%[2]s
WorkingDirectory=%[3]s
ExecStart=%[4]s
%[5]sRestart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// systemdServicePath est le PATH que systemd donne à une unité quand le fichier
// d'unité n'en pose pas : celui de DefaultEnvironment, défini à la compilation de
// systemd. On le recopie en dur pour le COMPLÉTER — un Environment=PATH remplace
// la valeur par défaut, jamais ne s'y ajoute, et un ajean qui ne trouve plus
// git, cmake ni node depuis son service est plus cassé qu'avant.
const systemdServicePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"

// rocmServiceEnv rend le bloc d'environnement d'une unité systemd pour ROCm :
// les variables qu'un moteur llama.cpp compilé en HIP exige, et que la machine
// ne fournit nulle part dans le contexte d'un service.
//
// Le problème, mesuré sur cette machine AMD (ROCm 7.2, CMake 4.5) : ROCm se pose
// dans /opt/rocm, absent du PATH d'une session COMME du PATH d'un service
// (systemctl show ajean-engine -p Environment → vide). Conséquences réelles :
//
//   - la compilation du moteur : CMakeDetermineHIPCompiler de CMake appelle
//     « hipconfig » EN NU trois fois (--platform, --rocmpath, --hipclangpath) pour
//     trouver la plateforme, la racine et le compilateur HIP. Sans /opt/rocm/bin
//     au PATH, hipconfig lui échappe : « Failed to find HIP root directory », ou
//     pire un clang du système retenu comme compilateur HIP, qui ne connaît pas
//     le target amdgcn. Avec /opt/rocm/bin au PATH, CMake découvre
//     /opt/rocm/lib/llvm/bin/clang++ tout seul et le build aboutit (vérifié).
//   - la télémétrie GPU de l'UI : amd-smi et rocm-smi vivent dans /opt/rocm/bin
//     (ici : aucun des deux dans le PATH du service) → la carte n'apparaît pas.
//   - ROCM_PATH, la variable que lit ggml-hip/CMakeLists.txt pour
//     CMAKE_PREFIX_PATH → find_package(hip REQUIRED) : posée, elle couvre les
//     racines non standard (/opt/rocm-6.4, une racine hors /opt).
//   - LD_LIBRARY_PATH : /opt/rocm/lib est souvent enregistré dans
//     /etc/ld.so.conf.d (ici oui), mais pas sur toutes les installations (wheels
//     TheRock, racine démontée) : sans lui, le moteur meurt à la charge de
//     libamdhip64/librocblas.
//
// Retourne "" quand aucune racine ROCm n'existe : on n'arrose pas d'Environment=
// les unités d'une machine NVIDIA, Intel ou Apple.
func rocmServiceEnv(root string) string {
	if root == "" {
		return ""
	}
	var dirs []string
	if d := filepath.Join(root, "bin"); isDir(d) {
		dirs = append(dirs, d)
	}
	if d := rocmLlvmBinLocal(root); d != "" {
		dirs = append(dirs, d)
	}
	path := systemdServicePath
	if len(dirs) > 0 {
		path = strings.Join(dirs, ":") + ":" + path
	}
	out := "Environment=PATH=" + path + "\n"
	out += "Environment=ROCM_PATH=" + root + "\n"
	if libs := rocmLdLibDirs(root); len(libs) > 0 {
		out += "Environment=LD_LIBRARY_PATH=" + strings.Join(libs, ":") + "\n"
	}
	return out
}

func installServices(targetUser, ajeanHome string) error {
	svc, exe := serviceName(), installedExePath()
	env := rocmServiceEnv(rocmRoot())
	units := map[string]string{
		svc:        fmt.Sprintf(engineUnitTemplate, targetUser, ajeanHome, exe+" serve", env),
		uiUnitName: fmt.Sprintf(uiUnitTemplate, svc, targetUser, ajeanHome, exe+" web", env),
	}
	for name, body := range units {
		path := "/etc/systemd/system/" + name + ".service"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("  %s %s\n", green("✓"), path)

		sudoers := fmt.Sprintf(sudoersTemplate, targetUser, name)
		sudoersPath := "/etc/sudoers.d/" + name
		if err := os.WriteFile(sudoersPath, []byte(sudoers), 0o440); err != nil {
			return err
		}
		fmt.Printf("  %s %s\n", green("✓"), sudoersPath)
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

// uninstallServices arrête, désactive et efface les deux unités. L'interface
// passe AVANT le moteur : elle sait le redémarrer, l'ordre inverse pourrait le
// relancer sous nos pieds.
func uninstallServices() {
	svc := serviceName()
	for _, name := range []string{uiUnitName, svc} {
		_ = exec.Command("systemctl", "stop", name).Run()
		_ = exec.Command("systemctl", "disable", name).Run()
	}
	for _, p := range []string{
		"/etc/systemd/system/" + svc + ".service",
		"/etc/systemd/system/" + uiUnitName + ".service",
		"/etc/sudoers.d/" + svc,
		"/etc/sudoers.d/" + uiUnitName,
	} {
		if err := os.Remove(p); err == nil {
			fmt.Printf("  %s %s\n", green("✓"), p)
		}
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
}
