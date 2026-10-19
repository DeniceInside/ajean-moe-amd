package ajean

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// web_external.go — endpoints /api/preset/external/* : lire, enregistrer et
// tester un preset « externe » (API OpenAI-compatible distante). L'UI les pilote
// depuis la modale ouverte par le bouton wifi, à gauche du « + » des presets.

// handlePresetExternal renvoie les champs d'un preset externe pour préremplir la
// modale d'édition. id vide = nouveau preset (champs vides).
func handlePresetExternal(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		sendJSON(w, 200, map[string]any{"id": "", "name": "", "url": "", "model": "", "hasKey": false})
		return
	}
	content, err := ReadPreset(id)
	if err != nil {
		sendJSON(w, 404, map[string]any{"error": "not found"})
		return
	}
	cfg := parseEnv(content)
	if !isExternalConfig(cfg) {
		sendJSON(w, 400, map[string]any{"error": "pas un preset externe"})
		return
	}
	// La clé n'est PAS renvoyée en clair : on signale seulement sa présence, pour
	// que la modale montre « clé enregistrée » sans l'exposer. Un ré-enregistrement
	// sans toucher au champ clé la conserve (voir handlePresetExternalSave).
	sendJSON(w, 200, map[string]any{
		"id":     id,
		"name":   presetDisplayName(content, id),
		"url":    strings.TrimSpace(cfg[extKeyURL]),
		"model":  strings.TrimSpace(cfg[extKeyModel]),
		"ctx":    strings.TrimSpace(cfg["CTX"]),
		"vision": strings.TrimSpace(cfg[extKeyVision]) == "1",
		"hasKey": strings.TrimSpace(cfg[extKeyToken]) != "",
		// Réveil Wake on LAN : la MAC n'est pas un secret (elle s'obtient par ARP
		// sur le LAN), elle est donc renvoyée pour préremplir la modale.
		"wol":      strings.TrimSpace(cfg[wolKeyFlag]) == "1",
		"wol_mac":  strings.TrimSpace(cfg[wolKeyMAC]),
		"wol_port": strings.TrimSpace(cfg[wolKeyPort]),
		"wol_wait": strings.TrimSpace(cfg[wolKeyWait]),
	})
}

// externalSaveReq : payload de la modale externe. `key` vide sur une ÉDITION
// conserve la clé existante (voir keyTouched) ; sur une création, absence de clé
// = pas d'authentification.
type externalSaveReq struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Model      string `json:"model"`
	Key        string `json:"key"`
	Ctx        string `json:"ctx"`        // taille de contexte (clé CTX), optionnelle
	Vision     bool   `json:"vision"`     // le modèle distant accepte les images
	KeyTouched bool   `json:"keyTouched"` // l'utilisateur a modifié le champ clé
	WOL        bool   `json:"wol"`        // réveiller la cible (Wake on LAN)
	WOLMAC     string `json:"wol_mac"`    // MAC de la machine du LAN
	WOLPort    string `json:"wol_port"`   // port UDP de la trame ("" = 7)
	WOLWait    string `json:"wol_wait"`   // secondes d'attente du port ("" = 90)
}

func handlePresetExternalSave(w http.ResponseWriter, r *http.Request) {
	var req externalSaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.Model = strings.TrimSpace(req.Model)
	if req.Name == "" {
		sendJSON(w, 400, map[string]any{"ok": false, "error": "nom requis"})
		return
	}
	if req.URL == "" {
		sendJSON(w, 400, map[string]any{"ok": false, "error": "URL requise"})
		return
	}
	if req.Model == "" {
		sendJSON(w, 400, map[string]any{"ok": false, "error": "modèle requis"})
		return
	}
	// Clé : sur une édition sans toucher au champ, on relit la clé déjà stockée
	// pour ne pas l'effacer (la modale ne la reçoit jamais en clair).
	key := req.Key
	if req.ID != "" && !req.KeyTouched {
		if content, err := ReadPreset(req.ID); err == nil {
			key = strings.TrimSpace(parseEnv(content)[extKeyToken])
		}
	}
	// Réveil Wake on LAN : une MAC exigée si l'interrupteur est posé, et une cible
	// qui n'est PAS du LAN refusée. Un preset API publique (OpenAI, Groq…) avec
	// WOL=1 enverrait un paquet magique sur la diffusion LOCALE de la machine qui
	// fait tourner AJEAN — vers des machines qui n'ont rien à voir avec la cible.
	wol := wolKeys{Enabled: req.WOL, MAC: req.WOLMAC, Port: req.WOLPort, Wait: req.WOLWait}
	if req.WOL {
		mac, err := wolNormalizeMAC(req.WOLMAC)
		if err != nil {
			sendJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		wol.MAC = mac
		if h, _, err := wolHostPort(req.URL); err != nil || wolGuard(h) != nil {
			sendJSON(w, 200, map[string]any{"ok": false, "error": "le Wake on LAN demande une adresse de LAN dans l'URL de l'API (l'URL actuelle n'en est pas une)"})
			return
		}
	}
	content := externalPresetContentWOL(req.URL, req.Model, key, req.Ctx, req.Vision, wol)
	// Réglages de MACHINE repris dans le preset : sans eux, la bascule vers ce
	// preset effaçait BIN de la config vive (voir externalMachineKeys).
	content = externalPresetWithMachine(content, externalMachineKeys(req.ID))
	// EXTERNAL_SERVICE n'a pas de champ dans la modale : une édition le conserve.
	if req.ID != "" {
		if old, err := ReadPreset(req.ID); err == nil {
			if svc := strings.TrimSpace(parseEnv(old)[extKeyService]); svc != "" {
				content = strings.TrimRight(content, "\n") + "\n" + extKeyService + "=" + svc + "\n"
			}
		}
	}
	newID, err := SavePreset(req.ID, req.Name, content)
	if err != nil {
		sendJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true, "id": newID, "name": req.Name})
}

// handlePresetExternalTest tente un appel de complétion minimal vers l'endpoint
// saisi, pour valider URL + modèle + clé avant d'enregistrer. Sur une édition
// sans clé fournie, on réutilise la clé stockée.
func handlePresetExternalTest(w http.ResponseWriter, r *http.Request) {
	var req externalSaveReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	url := completionsURL(req.URL)
	model := strings.TrimSpace(req.Model)
	if url == "" || model == "" {
		sendJSON(w, 200, map[string]any{"ok": false, "error": "URL et modèle requis"})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" && req.ID != "" && !req.KeyTouched {
		if content, err := ReadPreset(req.ID); err == nil {
			key = strings.TrimSpace(parseEnv(content)[extKeyToken])
		}
	}
	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		sendJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	if key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		sendJSON(w, 200, map[string]any{"ok": false, "error": "injoignable : " + err.Error()})
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(rb))
		if m := extractAPIError(rb); m != "" {
			msg = m
		}
		if msg == "" {
			msg = resp.Status
		}
		sendJSON(w, 200, map[string]any{"ok": false, "status": resp.StatusCode, "error": msg})
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true})
}

// extractAPIError sort le champ error.message d'une réponse d'erreur au format
// OpenAI ({"error":{"message":"…"}}), pour un message lisible plutôt que le JSON
// brut. Renvoie "" si le format ne correspond pas.
func extractAPIError(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		return strings.TrimSpace(e.Error.Message)
	}
	return ""
}

// handlePresetExternalWOL — POST /api/preset/external/wol : envoyer le paquet
// magique sur DEMANDE EXPLICITE (le bouton de la modale, « envoyer le paquet
// magique »), et dire ce qui s'est passé. C'est le seul chemin de l'interface qui
// réveille : la sonde de santé, elle, ne réveille jamais (wolActive n'existe que
// pour afficher un réveil déjà en vol, lancé par une requête de chat).
//
// La cible est l'IP de l'URL du preset, ou celle saisie pour le test. Le garde-fou
// d'adresse s'applique ici comme partout : une IP publique, un nom, refusés.
type wolSendReq struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`   // cible du test ; vide = l'hôte de l'URL saisie
	URL  string `json:"url"`  // l'URL de la modale : c'est elle qui donne le port du service
	Port string `json:"port"` // port UDP de la trame (7 par défaut)
	Wait string `json:"wait"` // secondes d'attente du port
}

func handlePresetExternalWOL(w http.ResponseWriter, r *http.Request) {
	var req wolSendReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	mac, err := wolNormalizeMAC(req.MAC)
	if err != nil {
		sendJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// L'URL de la modale donne le port du service à attendre ; le champ IP, quand
	// il est rempli, teste une AUTRE machine du LAN avec ce même port.
	_, port, uerr := wolHostPort(req.URL)
	host := strings.TrimSpace(req.IP)
	if host == "" {
		h, _, err := wolHostPort(req.URL)
		if err != nil {
			sendJSON(w, 200, map[string]any{"ok": false, "error": "IP de la cible requise (l'URL de l'API ne la donne pas)"})
			return
		}
		host = h
	}
	if host != "" && port == "" {
		port = "8080"
	}
	_ = uerr
	tt, _ := wolOf(map[string]string{wolKeyFlag: "1", wolKeyMAC: mac, wolKeyPort: req.Port, wolKeyWait: req.Wait})
	if tt.MAC == "" {
		sendJSON(w, 200, map[string]any{"ok": false, "error": "MAC invalide"})
		return
	}
	if err := wakeWOL(r.Context(), host, port, tt); err != nil {
		sendJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true, "detail": fmt.Sprintf("%s · %s · %s", host, mac, tt.MAC)})
}
