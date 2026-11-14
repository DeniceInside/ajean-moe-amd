Première release du **fork AMD/MoE** de AJEAN. Base synchronisée avec l'amont : **v0.18.17**. Le fork publie ses propres releases, et un binaire installé depuis le fork **reste sur le fork**.

## Ce que porte ce fork

- **Cartes AMD Radeon sous Linux (backend HIP).** Le moteur llama.cpp est compilé en HIP pour la carte présente, y compris RDNA4 (gfx1200 / gfx1201 : RX 9060 XT, RX 907 XT, AI PRO R9700). Les chemins ROCm des configurations du moteur sont redressés sur l'installation réelle, et la liste des architectures AMD est décidée par la carte, pas par la seule VRAM.
- **ROCm trouvé depuis un service systemd.** Ni le PATH d'une session ni celui d'un service systemd ne contiennent `/opt/rocm/bin` : CMake appelle `hipconfig` en nu trois fois pour découvrir ROCm, et `rocm-smi` / `amd-smi` (télémétrie GPU de l'interface) y vivent. `ajean install` pose désormais `ROCM_PATH`, `PATH` et `LD_LIBRARY_PATH` dans les unités `ajean-engine` et `ajean-ui` — sans quoi le build HIP échoue à « HIP compiler identification is unknown » et la carte n'apparaît nulle part.
- **Détection GPU par topologie KFD.** Quand `amd-smi` et `rocm-smi` sont absents (le cas sur beaucoup de distributions AMD), les cartes sont lues dans `/sys/devices/virtual/kfd/kfd/topology/nodes/` : nom, architecture, VRAM totale et utilisée, y compris sur les APU à mémoire unifiée. C'est le repli qui affiche la RX 9060 XT et la RX 907 XT là où l'amont affichait une liste vide.
- **Wake on LAN pour les presets « API externe ».** Le PC du réseau local qui sert une API peut être éteint : AJEAN envoie le paquet magique, apprend l'adresse MAC dès que la machine a répondu, et le réveil est piloté depuis les presets.
- **Moteur MoE.** Le moteur multi-plateformes (Strata) est géré sur la voie AMD, en plus de CUDA.
- **Moteur ExLlama (ExLlamaV3 / TabbyAPI) retiré** de ce fork.

## Identité : le fork s'annonce

- **`ajean update` consulte les releases du fork**, plus celles de l'amont. Pointé sur l'amont, un binaire de fork se proposait « 0.20.1 » et remplaçait le binaire du fork (HIP/AMD, MoE, KFD, WOL) par le binaire amont, qui ne les a pas — sans que personne ne demande la carte AMD.
- **La fenêtre s'appelle AJEAN-MOE-AMD** : titre de la fenêtre du lanceur, nom de l'app ajoutée à l'écran d'accueil, nom installé par le manifeste, titre de la notification « réponse prête », nom du service, nom et version dans les propriétés du `.exe` Windows, nom de l'app macOS. Deux binaires peuvent tourner sur la même machine, sur le même port 8090 : sans nom distinctif, impossible de savoir lequel répond. La marque AJEAN reste partout ailleurs.

## Installation depuis le fork

```bash
curl -L -o ajean https://github.com/DeniceInside/ajean-moe-amd/releases/latest/download/ajean-linux
chmod +x ajean && sudo mv ajean /usr/local/bin/ajean
sudo ajean install   # régénère les unités, avec l'environnement ROCm
ajean llamacpp install
ajean start
```

Le moteur MoE lui-même reste téléchargé depuis les releases de l'amont (tag `moe-v1.1`) : c'est le seul endroit où il est publié, ce fork ne compile pas le moteur, il l'installe et l'adapte.

## À savoir

- ROCm doit être installé sur la machine (`/opt/rocm`, avec `hipconfig` et le compilateur HIP).
- Les corrections de la série amont 0.18.x (dont la 0.18.17) sont comprises dans cette base.
- Cette release n'a pas été rejouée sur les machines des rapports amont cités plus bas.
