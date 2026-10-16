package ajean

// backend_moe_kfd.go — détection des GPU AMD par la topologie KFD du kernel.
//
// Pourquoi : amd-smi et rocm-smi ne sont pas installés par défaut — sur une
// machine au pilote amdgpu propre, les deux peuvent être absents (vérifié sur
// CachyOS : la RX 9070 XT est bien là, /dev/kfd chargé, aucun des deux outils).
// L'installeur du moteur amont détecte les cartes AMD de la même façon : il lit
// la topologie KFD du kernel, sans ROCm. C'est donc la source PRINCIPALE de
// moeAMDGPUs ; amd-smi puis rocm-smi ne restent que des replis.
//
// La numérotation des nœuds GPU (ordre croissant, nœuds CPU sautés) est celle
// de HIP : l'index renvoyé est donc directement celui à passer à --gpu N.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// moeKFDNames : arch → nom, quand le kernel ne donne pas de product_name
// (même table que l'installeur du moteur).
var moeKFDNames = map[string]string{
	"gfx1100": "AMD Radeon RX 7900 series (gfx1100)",
	"gfx1101": "AMD Radeon RX 7800 XT / 7700 XT (gfx1101)",
	"gfx1102": "AMD Radeon RX 7600 / 7600 XT (gfx1102)",
	"gfx1200": "AMD Radeon RX 9060 series (gfx1200)",
	"gfx1201": "AMD Radeon RX 9070 series / AI PRO R9700 (gfx1201)",
	"gfx1030": "AMD Radeon RX 6800 / 6900 series (gfx1030)",
	"gfx1031": "AMD Radeon RX 6700 XT series (gfx1031)",
	"gfx1151": "AMD Radeon 8060S / 8050S / 8040S (Ryzen AI Max, Strix Halo, gfx1151)",
}

// moeKFDGPUs : les GPU AMD de la topologie KFD, en numérotation HIP.
// sysfs : racine de l'arbre (les tests passent un arbre factice).
// Les nœuds CPU (simd_count 0) sont sautés ; la VRAM vient de
// mem_info_vram_total (octets) du device DRM du nœud.
func moeKFDGPUs(sysfs string) []moeGPU {
	base := filepath.Join(sysfs, "class/kfd/kfd/topology/nodes")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var nodes []string
	for _, e := range entries {
		if e.IsDir() && isDigits(e.Name()) {
			nodes = append(nodes, e.Name())
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		a, _ := strconv.Atoi(nodes[i])
		b, _ := strconv.Atoi(nodes[j])
		return a < b
	})
	var out []moeGPU
	for _, n := range nodes {
		props, err := os.ReadFile(filepath.Join(base, n, "properties"))
		if err != nil {
			continue
		}
		p := map[string]string{}
		for _, line := range strings.Split(string(props), "\n") {
			if k, v, ok := strings.Cut(line, " "); ok {
				p[k] = strings.TrimSpace(v)
			}
		}
		ver, _ := strconv.Atoi(p["gfx_target_version"])
		simd, _ := strconv.Atoi(p["simd_count"])
		if ver == 0 || simd == 0 {
			continue // nœud CPU
		}
		arch := fmt.Sprintf("gfx%d%x%x", ver/10000, (ver/100)%100, ver%100)
		dev := filepath.Join(sysfs, "class/drm/renderD"+p["drm_render_minor"], "device")
		vram := 0.0
		if b, err := os.ReadFile(filepath.Join(dev, "mem_info_vram_total")); err == nil {
			if bts, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				vram = float64(bts) / (1 << 30)
			}
		}
		name := ""
		if b, err := os.ReadFile(filepath.Join(dev, "product_name")); err == nil {
			name = strings.TrimSpace(string(b))
		}
		if name == "" {
			name = moeKFDNames[arch]
			if name == "" {
				name = "AMD Radeon (" + arch + ")"
			}
		}
		out = append(out, moeGPU{Index: len(out), Name: name, VRAMGB: vram})
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
