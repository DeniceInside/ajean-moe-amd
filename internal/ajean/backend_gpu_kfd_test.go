package ajean

import (
	"os"
	"path/filepath"
	"testing"
)

// kfdWrite : écrit un fichier sysfs factice (les tests de KFD n'ont pas de
// helper mustWrite commun dans la 0.19).
func kfdWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestKFDVramGPUs(t *testing.T) {
	// arbre factice : un nœud CPU (0) puis un nœud GPU (1) — 9070 XT, 16 Go,
	// comme la topologie KFD d'une machine amdgpu sans amd-smi ni rocm-smi
	root := t.TempDir()
	kfd := filepath.Join(root, "class/kfd/kfd/topology/nodes")
	kfdWrite(t, filepath.Join(kfd, "0", "properties"), "simd_count 0\ngfx_target_version 0\ndrm_render_minor 128\n")
	kfdWrite(t, filepath.Join(kfd, "1", "properties"), "simd_count 128\ngfx_target_version 120001\ndrm_render_minor 129\n")
	dev := filepath.Join(root, "class/drm/renderD129/device")
	kfdWrite(t, filepath.Join(dev, "mem_info_vram_total"), "17095983104\n")
	kfdWrite(t, filepath.Join(dev, "mem_info_vram_used"), "59916288\n")
	kfdWrite(t, filepath.Join(dev, "product_name"), "Radeon Graphics\n")

	gpus := kfdVramGPUs(root)
	if len(gpus) != 1 {
		t.Fatalf("%d carte(s), attendu 1 (le nœud CPU doit être sauté) : %+v", len(gpus), gpus)
	}
	g := gpus[0]
	if g["name"] != "Radeon Graphics" {
		t.Errorf("nom : %q", g["name"])
	}
	if g["total"] != 16304 {
		t.Errorf("total (MiB) : %v, attendu 16304 (17095983104 octets)", g["total"])
	}
	used, _ := g["used"].(int)
	if used < 55 || used > 65 {
		t.Errorf("used (MiB) : %v, attendu ~57", used)
	}
	if g["arch"] != "gfx1201" {
		t.Errorf("arch : %q", g["arch"])
	}
	if _, ok := g["dedicated"]; ok {
		t.Errorf("une carte dédiée ne doit pas porter « dedicated » : %+v", g)
	}

	// sans product_name : le nom vient de la table par arch (gfx1201)
	root2 := t.TempDir()
	kfd2 := filepath.Join(root2, "class/kfd/kfd/topology/nodes")
	kfdWrite(t, filepath.Join(kfd2, "1", "properties"), "simd_count 64\ngfx_target_version 120001\ndrm_render_minor 129\n")
	dev2 := filepath.Join(root2, "class/drm/renderD129/device")
	kfdWrite(t, filepath.Join(dev2, "mem_info_vram_total"), "17095983104\n")
	gpus = kfdVramGPUs(root2)
	if len(gpus) != 1 || gpus[0]["name"] != "AMD Radeon RX 9070 series / AI PRO R9700 (gfx1201)" {
		t.Errorf("%+v", gpus)
	}

	// topologie absente : rien
	if got := kfdVramGPUs(t.TempDir()); got != nil {
		t.Errorf("%+v", got)
	}

	// nœud GPU sans VRAM lisible : sauté (pas de « carte 0 Go »)
	root3 := t.TempDir()
	kfd3 := filepath.Join(root3, "class/kfd/kfd/topology/nodes")
	kfdWrite(t, filepath.Join(kfd3, "1", "properties"), "simd_count 64\ngfx_target_version 120001\ndrm_render_minor 129\n")
	if got := kfdVramGPUs(root3); got != nil {
		t.Errorf("%+v", got)
	}
}

func TestKFDVramGPUsStrixHalo(t *testing.T) {
	// un Strix Halo (gfx1151, 8060S = 80 SIMD) avec un carve-out BIOS de 2 Go
	// et un pool GTT de 30 Go : le nom vient du nombre de CU, la mémoire du GPU
	// est la somme carve-out + partagée, et « dedicated » est le carve-out seul.
	// gfx_target_version 110501 = major 11, minor 5, step 1.
	root := t.TempDir()
	kfd := filepath.Join(root, "class/kfd/kfd/topology/nodes")
	kfdWrite(t, filepath.Join(kfd, "1", "properties"), "simd_count 80\ngfx_target_version 110501\ndrm_render_minor 129\n")
	dev := filepath.Join(root, "class/drm/renderD129/device")
	kfdWrite(t, filepath.Join(dev, "mem_info_vram_total"), "2147483648\n")
	kfdWrite(t, filepath.Join(dev, "mem_info_gtt_total"), "32212254720\n")
	gpus := kfdVramGPUs(root)
	if len(gpus) != 1 {
		t.Fatalf("%d carte(s) : %+v", len(gpus), gpus)
	}
	g := gpus[0]
	if g["arch"] != "gfx1151" {
		t.Errorf("arch : %q", g["arch"])
	}
	ded, ok := g["dedicated"].(int)
	if !ok {
		t.Fatal("l'APU doit porter « dedicated »")
	}
	if ded < 1945 || ded > 2048 {
		t.Errorf("carve-out (MiB) : %v", ded)
	}
	// la mémoire du GPU est le carve-out plus la part partagée (le pool GTT de
	// 30 Go, moins ce qui reste à l'OS sur la machine qui fait le test)
	wantShared := min(30.0, totalRAMGB()-kfdUMAOSLeftGB)
	if wantShared > 0 {
		wantTotal := (2.0 + wantShared) * 1024
		if tot := g["total"].(int); tot < int(wantTotal*0.95) || tot > int(wantTotal*1.05) {
			t.Errorf("mémoire GPU : %v (le pool partagé n'est pas ajouté), attendu ~%v", tot, wantTotal)
		}
	}
	// sans product_name : le nom vient du nombre de CU
	if g["name"] != "AMD Radeon 8060S (Ryzen AI Max, Strix Halo, gfx1151)" {
		t.Errorf("nom : %q", g["name"])
	}
}

func TestKFDApplyUMA(t *testing.T) {
	// carve-out 2 Go, pool GTT 30 Go, RAM 64 Go → GPU voit 2 + min(30, 64-6)
	vram, dedicated := kfdApplyUMA(2, 30, 64)
	if dedicated != 2 {
		t.Errorf("dedicated : %v", dedicated)
	}
	if vram != 32 {
		t.Errorf("vram : %v, attendu 32", vram)
	}
	// sans pool GTT lisible : la moitié de la RAM (sans borne, 64/2 = 32 > 64-6)
	vram, _ = kfdApplyUMA(2, 0, 64)
	if vram != 2+32 {
		t.Errorf("vram : %v, attendu 34", vram)
	}
	// RAM minuscule : la part partagée est bornée à zéro
	vram, _ = kfdApplyUMA(2, 30, 4)
	if vram != 2 {
		t.Errorf("vram : %v, attendu 2", vram)
	}
}

func TestKFDIsDigits(t *testing.T) {
	for _, s := range []string{"0", "1", "12", "129"} {
		if !kfdIsDigits(s) {
			t.Errorf("%q doit être des chiffres", s)
		}
	}
	for _, s := range []string{"", "a", "1x", "-1", "1 2"} {
		if kfdIsDigits(s) {
			t.Errorf("%q ne doit pas être des chiffres", s)
		}
	}
}
