package ajean

import "testing"

// En mode hors RAM (carte d'aide sans partage des couches), le contexte enregistré
// est celui qui tourne : moeBuildConfig le ramènerait de toute façon à moeDropMaxCtx.
func TestMoeApplySettingsDropCap(t *testing.T) {
	in := "ENGINE=moe\nMOE_HELPER_GPU=0\nMOE_DROP=1\nCTX=131072\n"
	req := moeSettingsReq{Ctx: 167936, KV: "fp16", Spec: 3, Vision: true, Helper: true, Split: "helper"}
	out, err := moeApplySettings(in, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseEnv(out)["CTX"]; got != "131072" {
		t.Errorf("hors RAM : CTX=%s, attendu 131072", got)
	}
	// partage des couches : pas de mode hors RAM, la valeur demandée reste
	req.Split = "layers"
	out, _ = moeApplySettings(in, req)
	if got := parseEnv(out)["CTX"]; got != "167936" {
		t.Errorf("partage des couches : CTX=%s, attendu 167936", got)
	}
	// sans MOE_DROP non plus
	req.Split = "helper"
	out, _ = moeApplySettings("ENGINE=moe\nMOE_HELPER_GPU=0\n", req)
	if got := parseEnv(out)["CTX"]; got != "167936" {
		t.Errorf("sans hors RAM : CTX=%s, attendu 167936", got)
	}
}
