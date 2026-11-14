package ajean

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkRocm fabrique une fausse racine ROCm dans un dossier temporaire.
// complete=true : avec la config CMake de HIP (ce qui rend le build possible) ;
// complete=false : des dossiers vides, comme une installation amputée.
func mkRocm(t *testing.T, complete bool) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"bin", filepath.Join("lib", "llvm", "bin")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if complete {
		touch(filepath.Join("lib", "cmake", "hip", "hip-config.cmake"))
		touch(filepath.Join("lib", "cmake", "hip", "hip-config-version.cmake"))
	}
	touch(filepath.Join("bin", "hipcc"))
	touch(filepath.Join("bin", "hipconfig"))
	touch(filepath.Join("lib", "llvm", "bin", "clang++"))
	if err := os.MkdirAll(filepath.Join(root, ".info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".info", "version"), []byte("7.2.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRocmRootFromTool(t *testing.T) {
	if got := rocmRootFromTool("/opt/rocm/bin/hipcc"); got != "/opt/rocm" {
		t.Errorf("rocmRootFromTool(/opt/rocm/bin/hipcc) = %q", got)
	}
	if got := rocmRootFromTool("/usr/bin/hipcc"); got != "/usr" {
		t.Errorf("racine système : %q, attendu /usr", got)
	}
	for _, bad := range []string{"", "/opt/rocm/hipcc", "hipcc"} {
		if got := rocmRootFromTool(bad); got != "" {
			t.Errorf("rocmRootFromTool(%q) = %q, attendu \"\"", bad, got)
		}
	}
}

func TestRocmRootIsSound(t *testing.T) {
	if rocmRootIsSound("") {
		t.Error("racine vide acceptée")
	}
	root := mkRocm(t, false)
	if rocmRootIsSound(root) {
		t.Error("installation sans config CMake HIP acceptée")
	}
	full := mkRocm(t, true)
	if !rocmRootIsSound(full) {
		t.Error("installation complète refusée")
	}
}

func TestRocmRootPrefereCelleQuiConfigure(t *testing.T) {
	amputee := mkRocm(t, false)
	if rocmRootIsSound(amputee) {
		t.Fatal("racine amputée sound ?")
	}
	t.Setenv("ROCM_PATH", amputee)
	t.Setenv("HIP_PATH", "")
	// Sur une machine qui a /opt/rocm complet, la racine qui CONFIGURE doit
	// l'emporter sur celle que l'utilisateur a mise dans ROCM_PATH : c'est elle
	// qui évite le « package "hip" not found » au configure.
	if isDir("/opt/rocm") && rocmRootIsSound("/opt/rocm") {
		if got := rocmRoot(); got != "/opt/rocm" {
			t.Errorf("rocmRoot() = %q, attendu /opt/rocm (seule racine à configurer CMake)", got)
		}
	}
}

func TestRocmRootEnvironnementPrioritaire(t *testing.T) {
	full := mkRocm(t, true)
	t.Setenv("ROCM_PATH", full)
	t.Setenv("HIP_PATH", "")
	if got := rocmRoot(); got != filepath.Clean(full) {
		t.Errorf("rocmRoot() = %q, attendu %q (la ROCM_PATH de l'utilisateur, racine sound)", got, filepath.Clean(full))
	}
	// ROCM_PATH pointing at a directory that does not exist: ignored.
	t.Setenv("ROCM_PATH", filepath.Join(full, "nope"))
	for _, c := range rocmCandidates() {
		if c == filepath.Join(full, "nope") {
			t.Error("ROCM_PATH morte retenue comme candidate")
		}
	}
}

func TestRocmLlvmBinLesDeuxImplantations(t *testing.T) {
	root := t.TempDir()
	if rocmLlvmBinLocal(root) != "" {
		t.Error("dossier vide accepté")
	}
	// Implantation ROCm ≤ 6.x : <root>/lib/llvm/bin
	lib := filepath.Join(root, "lib", "llvm", "bin")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := rocmLlvmBinLocal(root); got != lib {
		t.Errorf("lib/llvm/bin : %q, attendu %q", got, lib)
	}
	// Implantation TheRock / wheel : <root>/llvm/bin
	root2 := t.TempDir()
	alt := filepath.Join(root2, "llvm", "bin")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := rocmLlvmBinLocal(root2); got != alt {
		t.Errorf("llvm/bin : %q, attendu %q", got, alt)
	}
	if rocmLlvmBinLocal("") != "" {
		t.Error("racine vide acceptée")
	}
}

func TestRocmHipCompiler(t *testing.T) {
	root := mkRocm(t, true)
	cc := rocmHipCompiler(root)
	if cc == "" {
		t.Skip("clang++ nommé clang++.exe sur cette plateforme (windows-only test)")
	}
	if filepath.Dir(cc) != rocmLlvmBinLocal(root) {
		t.Errorf("compilateur hors du clang de ROCm : %q", cc)
	}
	if rocmHipCompiler(t.TempDir()) != "" {
		t.Error("compilateur inventé là où il n'y a pas de llvm")
	}
}

func TestRocmVersion(t *testing.T) {
	root := mkRocm(t, true)
	if got := rocmVersion(root); got != "7.2.4" {
		t.Errorf("rocmVersion = %q, attendu 7.2.4", got)
	}
	if rocmVersion(t.TempDir()) != "" {
		t.Error("version inventée")
	}
}

func TestRocmBuildEnvExposeCeQueCMakeCherche(t *testing.T) {
	root := mkRocm(t, true)
	be := newBuildEnv()
	if !rocmBuildEnv(be, root) {
		t.Fatal("racine valide refusée")
	}
	out := be.serialize()
	vals := map[string]string{}
	for _, kv := range strings.Split(out, "\x00") {
		if i := strings.IndexByte(kv, '='); i > 0 {
			vals[kv[:i]] = kv[i+1:]
		}
	}
	// Les deux variables que les outils ROCm lisent, selon les versions.
	if vals["ROCM_PATH"] != root {
		t.Errorf("ROCM_PATH = %q, attendu %q", vals["ROCM_PATH"], root)
	}
	if vals["HIP_PATH"] != root {
		t.Errorf("HIP_PATH = %q", vals["HIP_PATH"])
	}
	// PATH : <root>/bin en tête, pour que CMake trouve hipconfig (il l'appelle en nu).
	pathKey := "PATH"
	if _, ok := vals[pathKey]; !ok {
		pathKey = "Path"
	}
	if !strings.HasPrefix(vals[pathKey], filepath.Join(root, "bin")) {
		t.Errorf("PATH ne démarre pas par <root>/bin : %q", vals[pathKey])
	}
	if !strings.Contains(vals[pathKey], rocmLlvmBinLocal(root)) {
		t.Error("le clang de ROCm n'est pas dans le PATH")
	}
	// Le compilateur HIP n'est PAS imposé : CMake le découvre via
	// « hipconfig --hipclangpath » (d'où le PATH posé plus haut). Mesuré : imposer
	// CMAKE_HIP_COMPILER fait échouer le configure quand l'identification ne peut
	// pas tourner. rocmHipCompiler sert à l'affichage du plan, pas à CMake.
	if v, ok := vals["CMAKE_HIP_COMPILER"]; ok {
		t.Errorf("CMAKE_HIP_COMPILER posé (%q) : l'imposition casse le configure", v)
	}
	if rocmHipCompiler(root) == "" {
		t.Error("le clang HIP de la racine n'est pas trouvé, le plan ne peut rien afficher")
	}
	// Les bibliothèques (rocBLAS, hipBLAS, amdhip64) accessibles au test de lien.
	libVar := "LD_LIBRARY_PATH"
	if _, ok := vals[libVar]; !ok {
		libVar = "DYLD_LIBRARY_PATH"
	}
	if !strings.HasPrefix(vals[libVar], filepath.Join(root, "lib")) {
		t.Errorf("%s = %q, attendu <root>/lib en tête", libVar, vals[libVar])
	}
	if rocmBuildEnv(newBuildEnv(), "") {
		t.Error("racine vide acceptée")
	}
}

// prependPathVar doit cumuler, pas écraser : un build peut poser CUDA et ROCm
// dans la même variable (et le MSVC de Windows y pose déjà la sienne).
func TestPrependPathVarCumule(t *testing.T) {
	be := newBuildEnv()
	be.prependPathVar("LD_LIBRARY_PATH", "/a")
	be.prependPathVar("LD_LIBRARY_PATH", "/b", "/c")
	be.prependPathVar("LD_LIBRARY_PATH", "") // rien à poser => ne touche pas
	want := "/b" + string(os.PathListSeparator) + "/c" + string(os.PathListSeparator) + "/a"
	got := ""
	for _, kv := range strings.Split(be.serialize(), "\x00") {
		if strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			got = strings.TrimPrefix(kv, "LD_LIBRARY_PATH=")
		}
	}
	if got != want {
		t.Errorf("LD_LIBRARY_PATH = %q, attendu %q", got, want)
	}
}

// Le plan doit retenir la racine ROCm : buildLlamacpp s'en sert pour poser
// ROCM_PATH, HIP_PATH, le PATH et CMAKE_HIP_COMPILER.
func TestBuildPlanHipRetientRacineRocm(t *testing.T) {
	full := mkRocm(t, true)
	t.Setenv("ROCM_PATH", full)
	t.Setenv("HIP_PATH", "")
	p := buildPlanFor("hip")
	if p.backend != "hip" {
		t.Fatalf("backend = %q, attendu hip", p.backend)
	}
	if p.rocmRoot != filepath.Clean(full) {
		t.Errorf("p.rocmRoot = %q, attendu %q", p.rocmRoot, filepath.Clean(full))
	}
	if p.rocmRoot != rocmRoot() {
		t.Errorf("le plan et rocmRoot() divergent : %q vs %q", p.rocmRoot, rocmRoot())
	}
	found := false
	for _, f := range p.flags {
		if f == "-DGGML_HIP=ON" {
			found = true
		}
	}
	if !found {
		t.Error("-DGGML_HIP=ON absent du plan")
	}
}
