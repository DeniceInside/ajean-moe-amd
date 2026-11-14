package ajean

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ROCm / HIP : détection de la racine du toolkit et préparation de l'environnement
// de build du backend HIP de llama.cpp.
//
// Pourquoi ce fichier existe. Un build HIP ne casse pas parce que ROCm est absent,
// il casse parce que ROCm est là SANS ÊTRE VISIBLE :
//
//   - ROCm se pose dans /opt/rocm, que ni le PATH par défaut ni le PATH d'un
//     service systemd ne contiennent. « hipcc introuvable » alors que
//     /opt/rocm/bin/hipcc existe, et surtout : CMake appelle « hipconfig » en nu
//     (CMakeDetermineHIPCompiler : hipconfig --platform, --rocmpath,
//     --hipclangpath) pour trouver la plateforme, la racine et le compilateur HIP.
//     Sans /opt/rocm/bin dans le PATH, ces appels échouent → « Failed to find HIP
//     root directory », ou un clang++ générique pris pour compilateur HIP.
//   - ggml/src/ggml-hip/CMakeLists.txt lit la racine dans la VARIABLE
//     d'environnement ROCM_PATH (puis /opt/rocm en repli) et s'en sert pour
//     CMAKE_PREFIX_PATH → find_package(hip REQUIRED). Le repli ne couvre ni les
//     installations versionnées (/opt/rocm-6.4) ni les racines hors /opt.
//
// On fournit donc la racine détectée explicitement : ROCM_PATH + HIP_PATH, le
// compilateur HIP clang de ROCm (jamais le clang++ du système), et le PATH de
// <root>/bin + <root>/lib/llvm/bin pour que hipconfig et rocm_agent_enumerator
// soient trouvés par CMake. C'est la même philosophie que le CUDA d'ajean, qui ne
// se repose jamais sur le PATH pour poser CUDAToolkit_ROOT (voir backend_build.go).

// rocmEnvRoots renvoie les racines ROCm indiquées par l'environnement
// (ROCM_PATH, le nom que les outils d'AMD lisent ; HIP_PATH, legacy).
func rocmEnvRoots() []string {
	var out []string
	for _, name := range []string{"ROCM_PATH", "HIP_PATH"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" && isDir(v) {
			out = append(out, filepath.Clean(v))
		}
	}
	return out
}

// rocmRootFromTool déduit une racine ROCm du chemin d'un de ses exécutables
// (<root>/bin/hipcc → <root>). "" si le chemin n'a pas cette forme.
func rocmRootFromTool(tool string) string {
	if tool == "" || filepath.Base(filepath.Dir(tool)) != "bin" {
		return ""
	}
	root := filepath.Dir(filepath.Dir(tool))
	switch filepath.ToSlash(root) {
	case "", ".", "/":
		return ""
	}
	return root
}

// rocmRootIsSound vérifie qu'une racine candidate EST un toolkit HIP complet, pas
// juste un dossier nommé rocm. Le critère : la config CMake de HIP, que
// find_package(hip REQUIRED) de ggml-hip va chercher. Sans elle, le configure
// échoue sur « Could not find a package configuration file provided by "hip" »,
// message parfaitement opaque pour un utilisateur qui a pourtant installé ROCm.
func rocmRootIsSound(root string) bool {
	if root == "" {
		return false
	}
	cmake := filepath.Join(root, "lib", "cmake", "hip")
	return isFile(filepath.Join(cmake, "hip-config.cmake")) ||
		isFile(filepath.Join(cmake, "hip-config-version.cmake"))
}

// rocmCandidates liste les racines ROCm plausibles, triées par version
// décroissante : variables d'environnement, /opt/rocm (installation standard,
// symlink posé par rocm-core), /opt/rocm-* (installations versionnées), racine
// déduite d'un hipcc/hipconfig joignable.
func rocmCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(root string) {
		root = filepath.Clean(root)
		if root == "." || !isDir(root) {
			return
		}
		key := strings.ToLower(root)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, root)
	}

	for _, r := range rocmEnvRoots() {
		add(r)
	}
	if isDir("/opt/rocm") {
		add("/opt/rocm")
	}
	versioned, _ := filepath.Glob("/opt/rocm-*")
	sortByVersionDesc(versioned)
	for _, d := range versioned {
		add(d)
	}
	for _, tool := range []string{"hipcc", "hipconfig"} {
		if p, err := exec.LookPath(tool); err == nil {
			add(rocmRootFromTool(p))
		}
	}
	return out
}

// rocmRoot choisit la racine ROCm à utiliser. Priorité absolue à celle dont la
// config CMake HIP est présente — elle seule rend le configure possible ; parmi
// elles, la plus récente. À défaut, la première candidate existante : la
// détection du backend (hasTool("hipcc") || isDir("/opt/rocm")) a déjà tranché
// pour HIP, on lui donne la racine la plus probable et hintMissingBuildDep
// expliquera ce qui manque. "" quand aucune racine n'existe.
func rocmRoot() string {
	var sound []string
	for _, r := range rocmCandidates() {
		if rocmRootIsSound(r) {
			sound = append(sound, r)
		}
	}
	if len(sound) > 0 {
		return sound[0] // rocmCandidates est triée par version décroissante
	}
	if cands := rocmCandidates(); len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// rocmLlvmBinLocal renvoie le dossier bin du clang livré avec ROCm. Le seul
// compilateur HIP de la plateforme amd : CMakeDetermineHIPCompiler cherche un
// « clang++ » et, s'il prend celui du système, ce clang ne connaît pas le target
// amdgcn. ROCm l'installe selon les versions dans <root>/lib/llvm/bin ou
// <root>/llvm/bin. "" si absent.
func rocmLlvmBinLocal(root string) string {
	if root == "" {
		return ""
	}
	for _, rel := range [][]string{{"lib", "llvm", "bin"}, {"llvm", "bin"}} {
		if d := filepath.Join(root, filepath.Join(rel...)); isDir(d) {
			return d
		}
	}
	return ""
}

// rocmHipCompiler désigne le compilateur HIP que ROCm fournit : son clang.
// Sert à AFFICHER le compilateur retenu dans le plan de build (l'utilisateur voit
// quelle toolchain compile ses kernels). On ne l'impose pas à CMake : voir la note
// dans rocmBuildEnv — l'imposition casse le configure, la découverte par hipconfig
// est plus tolérante.
func rocmHipCompiler(root string) string {
	bin := rocmLlvmBinLocal(root)
	if bin == "" {
		return ""
	}
	name := "clang++"
	if runtime.GOOS == "windows" {
		name = "clang++.exe"
	}
	if p := filepath.Join(bin, name); isFile(p) {
		return p
	}
	return ""
}

// rocmVersion lit la version d'une racine ROCm (.info/version, posé par
// rocm-core). Vide si inconnue.
func rocmVersion(root string) string {
	if root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, ".info", "version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Split(string(data), "\n")[0])
}

// rocmLdLibDirs énumère les dossiers de bibliothèques d'une racine ROCm, dans
// l'ordre où le chargeur doit les trouver. utile AUSSI au build : CMake teste le
// compilateur HIP en liant un programme, et ggml-hip lie rocBLAS, hipBLAS et le
// préprocesseur des kernels (hipfiletiler) — tous absents du cache du linker
// quand ROCm n'a pas été enregistré dans /etc/ld.so.conf.d.
func rocmLdLibDirs(root string) []string {
	if root == "" {
		return nil
	}
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" || seen[d] || !isDir(d) {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	add(filepath.Join(root, "lib"))
	add(rocmLlvmBinLocal(root))
	if runtime.GOOS == "linux" {
		// Paquets ROCm versionnés par famille de puces (gfx9xx, gfx1x…) :
		// lib/x86_64-linux-gfx11/.
		m, _ := filepath.Glob(filepath.Join(root, "lib", "x86_64-linux-gfx*"))
		sortByVersionDesc(m)
		for _, d := range m {
			add(d)
		}
	}
	return dirs
}

// rocmBuildEnv prépare l'environnement d'un build du backend HIP (voir l'en-tête
// du fichier). Retourne false si aucune racine n'est trouvable : le build part
// quand même — GGML_HIP=ON a été décidé par la détection — et l'aide sur le
// configure raté expliquera ce qui manque.
func rocmBuildEnv(be *buildEnv, root string) bool {
	if root == "" {
		return false
	}
	// ROCM_PATH : lu par ggml-hip/CMakeLists.txt pour CMAKE_PREFIX_PATH →
	// find_package(hip|hipblas|rocblas|rccl). HIP_PATH : lu par les paquets ROCm
	// eux-mêmes. Les deux, car ROCm lit l'un ou l'autre selon les outils.
	be.set("ROCM_PATH", root)
	be.set("HIP_PATH", root)

	// PATH : hipconfig (les trois appels de CMake), rocm_agent_enumerator (arch
	// du GPU), hipcc, et le clang de ROCm.
	var prepend []string
	if d := filepath.Join(root, "bin"); isDir(d) {
		prepend = append(prepend, d)
	}
	if d := rocmLlvmBinLocal(root); d != "" {
		prepend = append(prepend, d)
	}
	pathVar := "PATH"
	if runtime.GOOS == "windows" {
		pathVar = "Path"
	}
	be.prependPathVar(pathVar, prepend...)

	// LD_LIBRARY_PATH : lier le programme de test de CMake exige libamdhip64 et
	// rocBLAS/hipBLAS. Sur macOS la variable s'appelle DYLD_LIBRARY_PATH.
	libVar := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		libVar = "DYLD_LIBRARY_PATH"
	}
	be.prependPathVar(libVar, rocmLdLibDirs(root)...)

	// PAS de CMAKE_HIP_COMPILER, volontairement. CMake découvre le clang de ROCm
	// lui-même : CMakeDetermineHIPCompiler appelle « hipconfig --hipclangpath » et
	// retient le compilateur indiqué. Le filet, c'est donc le PATH posé au-dessus
	// (hipconfig doit être joignable). Mesuré sur la machine de test (CMake 4.5,
	// ROCm 7.2) : avec <root>/bin au PATH, CMake retient /opt/rocm/lib/llvm/bin/clang++
	// et le build aboutit ; imposer CMAKE_HIP_COMPILER=ce même clang fait échouer le
	// configure (« The HIP compiler identification is unknown → Configuring
	// incomplete ») dès que l'auto-détection d'identification ne peut pas tourner.
	// La découverte est plus tolérante que l'imposition : on ne force pas.
	return true
}

// hipRootHint explique l'échec d'un configure HIP dans un langage d'utilisateur,
// avec les chemins réels de la machine.
func hipRootHint(root string) {
	if root == "" {
		root = "/opt/rocm"
	}
	fmt.Printf("\n%s le backend %s est choisi, mais la racine ROCm %s n'a pas suffi à configurer le build.\n",
		yellow("[ROCm]"), bold("HIP"), root)
	fmt.Printf("            Ce que le build HIP exige : le toolkit complet (paquets « rocm-dev », « hip-dev », « hipblas-dev », « rocblas-dev »)\n")
	fmt.Printf("            — pas hipcc seul — avec sa config CMake dans %s.\n", bold(filepath.Join(root, "lib", "cmake", "hip")))
	fmt.Printf("            Vérifie que %s existe, puis relance %s.\n",
		bold(filepath.Join(root, "bin", "hipconfig")), bold("ajean llamacpp install"))
	fmt.Printf("            ROCm est installé ailleurs ? Lance avec ROCM_PATH=/chemin/rocm dans l'environnement d'ajean (voir %s).\n",
		bold("ajean service install"))
}
