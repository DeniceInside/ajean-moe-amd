Le moteur MoE — la famille Qwen3.8 Flash Next — s'installe sur les cartes AMD Radeon sous Linux.

## Cartes AMD sous Linux

Le moteur était réservé à NVIDIA : la détection ne regardait que `nvidia-smi`, et l'installeur ne posait que `--cuda`. Sur une machine sans carte NVIDIA, l'interface refusait de lancer la famille MoE (« ce moteur demande une carte NVIDIA »). Elle s'installe maintenant sur les cartes AMD Radeon : détection, compilation du backend HIP, lancement.

- **La détection vient de la topologie KFD du kernel**, pas des outils : `nvidia-smi`, `amd-smi`, `rocm-smi` et ROCm sont absents de la machine de test, et la carte doit être trouvée **avant** toute installation — c'est l'installeur du moteur qui pose ROCm. Repli sur `amd-smi`, puis `rocm-smi`, quand il n'y a pas de nœud KFD. L'ordre des nœuds GPU (les nœuds CPU sont sautés) est l'ordre HIP : l'index va directement à `--gpu N`.
- **La liste d'architectures décide, pas la mémoire de la carte.** Le backend HIP se compile pour une table d'architectures : gfx1030, gfx1031, gfx1100, gfx1101, gfx1102, gfx1151, gfx1200, gfx1201. Une MI300X (gfx942, 192 Go de HBM) passe le seuil de mémoire et ne se compile pas : promettre ~68 Go de téléchargement pour échouer à la compilation est le pire des deux faux. Une carte sans architecture lisible n'est pas proposée. `gfx1103` (Radeon 780M) n'est dans la liste amont qu'en opt-in : pas proposé.
- **Les iGPU sont exclus** : leur valeur « total » est une part de la RAM, pas de la VRAM. Seules les cartes à au moins 8 Go de mémoire **dédiée** sont retenues (le minimum d'ajean est 12 Go, vérifié à la détection).
- **Strix Halo (gfx1151)** : la mémoire lue par le kernel n'est que le carve-out du BIOS (2 Go sur les machines des mainteneurs), et le GPU atteint aussi le pool GTT (la moitié de la RAM). La mémoire partagée EST la RAM où vivent les experts : la mémoire de la carte est calculée en conséquence, la carte dédiée est choisie avant un APU (sa GDDR est plus rapide que la LPDDR5X partagée), et l'interface dit que la mémoire du GPU est la RAM et ne s'ajoute pas à elle.
- **Le moteur est compilé sur la machine** (`--backend hip`) : il n'existe pas de binaire AMD figé. L'installation est plus longue au premier lancement, et l'interface le dit.

## Machine de test

CachyOS (Arch, x86_64), noyau **7.2.9**, pilote **`amdgpu` du kernel** : RDNA4 est activé par défaut depuis Linux **6.11** (avant, il fallait `amdgpu.exp_hw_support=1`), et sans ce pilote il n'y a pas de nœud KFD — la détection ne trouve rien. RX 9070 XT **gfx1201**, 15,9 Go de VRAM, 70,6 Go de RAM, Python 3.14.8. **Aucun ROCm installé (pas de `/opt/rocm`), `amd-smi` et `rocm-smi` absents** : seule la voie KFD est exercée. Moteur compilé avec `-DCMAKE_HIP_ARCHITECTURES=gfx1201`, ~68 Go téléchargés, `backend: hip`, `gpu: 0` — **préfill 89,9 jetons par seconde, décodage 67,2 jetons par seconde**. ROCm 7.10 vient des wheels TheRock dans le venv (~10 Go, sans sudo).

Le cas qui fait la différence : la carte NVIDIA retirée de la même machine. Le binaire du 15 octobre, qui ne regardait que NVIDIA, répondait « ce moteur demande une carte NVIDIA » et l'interface refusait de lancer. Ce build retient la RX 9070 XT et le modèle se lance. C'est le scénario d'une machine qui n'a jamais eu de carte NVIDIA.

## Non testé

- **Les replis `amd-smi` et `rocm-smi`** : sur la machine de test la voie KFD répond toujours, ils ne sont jamais exercés en production. La clé d'architecture d'`amd-smi` est `asic.target_graphics_version` (la chaîne est déjà formatée, « gfx1201 ») — vérifiée dans la sortie du paquet 7.2.4 et dans son module Python.
- **Aucune autre carte AMD.** Les autres architectures de la liste n'ont été ni installées ni compilées.
- **Une distribution où `/dev/kfd` et les nœuds `renderD*` sont en mode 0660** (Debian, Ubuntu) : l'installeur du moteur refuse l'installation quand l'utilisateur ne peut pas ouvrir ces nœuds, et demande `sudo usermod -aG render,video $USER`. Sur la machine de test les nœuds sont en 0666, ce cas n'a pas été rencontré.
- **Un noyau plus ancien que 6.11** : sans nœud KFD, la détection ne trouve aucune carte.
- **L'installation à neuf, complète, sur une machine qui n'a jamais eu de carte NVIDIA** : la détection l'a été, pas le téléchargement de ~68 Go.

## Tests

`go test ./...`, gofmt, vet, build, cross-compile des 4 cibles de la release : au vert. 10 tests ajoutés, dont `TestMoeAMDCards` (iGPU exclu), `TestMoeAMDSupported` (MI300X et iGPU exclus, architecture hors liste refusée, ordre carte dédiée avant APU), `TestMoeKFDStrixHalo` (carve-out 2 Go + pool 30 Go, nom de carte par nombre d'unités de calcul), `TestMoeAMDArchs` (la liste affichée à l'utilisateur est la liste acceptée).

## Mise à jour

    ajean update
