```
█░   █▀█  █▀▀  ▄▀█  █░   █▀▀  █░█  █▀█  █▀▀  ▀█▀
█▄▄  █▄█  █▄▄  █▀█  █▄▄  █▄█  █▀█  █▄█  ▄▄█  ░█░
```

# THE ONLY CLOUD IS YOU

> *"If it can't run without their servers, you're a tenant."*  
> — [Why We Build](https://www.localghost.ai/manifesto)

Your data. Your hardware. Your ghost.

Read [why we build](https://www.localghost.ai/manifesto).

---

## Status: Phase 0

**Current state:** Website and vision. No code yet.

- [x] Website live at [localghost.ai](https://www.localghost.ai)
- [x] Manifesto published
- [x] Architecture documented
- [ ] First commit

---

## What Is This?

LocalGhost is a privacy-first, self-hosted AI system designed to run entirely on your hardware. No cloud. No subscriptions. No surveillance. Just a black box that works for you.

This repository will contain:
- Hardware specifications and bill of materials
- Daemon source code (six services that power the system)
- Docker configurations for deployment
- Installation scripts and upgrade tooling
- Documentation and architecture decisions

The [website](https://github.com/LocalGhostDao/web) lives in a separate repo.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        YOUR HARDWARE                            │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐                        │
│  │  SCRIBE  │ │ OBSERVER │ │ AUDITOR  │  ← INPUT DAEMONS       │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘                        │
│       │            │            │                               │
│       └────────────┼────────────┘                               │
│                    ▼                                            │
│             ┌──────────┐                                        │
│             │  WEAVER  │  ← SYNTHESIS                           │
│             └────┬─────┘                                        │
│                  │                                              │
│       ┌──────────┴──────────┐                                   │
│       ▼                     ▼                                   │
│  ┌──────────┐         ┌──────────┐                              │
│  │ SENTINEL │         │  SHADOW  │  ← OUTPUT                    │
│  └────┬─────┘         └──────────┘                              │
│       │                    ▲                                    │
│       ▼                    │                                    │
│   THE MIST (P2P)      YOU (human)                               │
└─────────────────────────────────────────────────────────────────┘

NOTHING LEAVES THE BOX UNLESS YOU ENABLE THE MIST.
```

---

## The Daemons

Six daemons. Each has one job. All communicate over local Unix sockets.

| Daemon | Role | Description |
|--------|------|-------------|
| THE SCRIBE | Text Ingestion | Indexes journals, notes, transcripts. Stores in Postgres with vector embeddings. |
| THE OBSERVER | Vision Pipeline | Processes camera/screen input. OCR, scene tagging, local image embeddings. Opt-in only. |
| THE AUDITOR | Metrics Collector | Plugin system for importing data. Bank CSVs, screen time, git history, health exports. |
| THE WEAVER | Correlation Engine | Runs inference. Correlates timestamps across data sources. Finds patterns via vector similarity. |
| THE SENTINEL | Encryption & Backup | FIDO2 key management, AES-256-GCM encryption. Local and P2P backup. Duress mode. |
| THE SHADOW | Query Interface | Web UI + API. Ask questions, get answers with citations to your own data. If you lie to it, it cites the Auditor to correct you. |

---

## Security Model

LocalGhost creates a searchable record of your life. Encryption protects data at rest — it doesn't protect you from a warrant, a wrench, or a border crossing.

We solve this with **hidden volumes** and **duress mode**:

| PIN | What Happens |
|-----|--------------|
| **Real PIN** | Full system. Your actual data. |
| **Duress PIN** | Shadow system. A different believable person — randomized patterns, bland content. |

The Observer generates shadow data daily — not a sanitized you, but a boring stranger who uses the same device. Forensic analysis finds an unremarkable person. The real volume stays hidden — indistinguishable from empty space.

A separate **Purge** function exists for when you need everything actually gone.

*Shadow system planned for v1.0+. Basic hidden volumes in earlier releases.*

**[Read the full security model →](docs/SECURITY.md)**

---

## Tech Stack

Simple and boring. Things we know work.

| Layer | Technology | Notes |
|-------|------------|-------|
| Core Services | Go | Single binary, no runtime deps |
| Inference | Python / llama.cpp | AI ecosystem lives there. We're not fighting it. |
| Database | Postgres + pgvector | Structured data + vector embeddings |
| Cache | Redis | Session state, pub/sub between daemons |
| Inference (v0.1) | External APIs or Ollama | Ship first, optimise later |
| Inference (v0.2+) | Ollama / llama.cpp | Local-first as default |
| IPC | Unix sockets + Redis pub/sub | Daemons talk locally |

No third-party cloud services required.

---

## Roadmap

Software releases named after ghosts, smallest to largest.

### Phase 0: "Foundation" — Now

Website and vision. This document.

---

### Phase 1: "Bones" — Months 1-3 → `wisp` (v0.1)

**Goal:** Write notes, ask questions about your own data.

- The Scribe — text ingestion, journaling
- The Weaver — RAG pipeline (pgvector + inference)
- The Shadow — web UI for queries
- Basic encryption (FIDO2 key unlock)
- Local backup to USB/NAS

Not included: The Mist, The Observer, hardware sales.

---

### Phase 2: "Senses" — Months 4-6 → `shade` (v0.2)

**Goal:** Multimodal inputs.

- The Auditor — plugin system for imports
- The Observer — camera/screen input, OCR (opt-in)
- Cross-source correlation
- Local-first inference default
- Mobile app (photo/health/location sync)
- Browser extension (bookmarks, reading history)
- S3-compatible backup (R2, Backblaze, MinIO)

---

### Phase 3: "Shell" — Months 6-9 → `specter` (v0.3)

**Goal:** Hardware ships after software is stable.

- Official `mini` and `core` reference designs
- Pre-built images for supported boards
- One-click installer
- Hardware validation test suite

---

### Phase 4: "Form" — Months 9-12 → `phantom` (v1.0)

**Goal:** Production-ready.

- The Sentinel — full key management
- `pro` and `rack` hardware tiers
- API stability guarantees
- Security audit

---

### Phase 5: "Mist" — Month 18+ → `poltergeist` (v3.0)

**Goal:** P2P backup for those who want it.

- The Mist — DHT, shard distribution
- Bootstrap node network
- NAT traversal, QUIC transport

P2P requires critical mass. Local backup works for years. The Mist is a long-term goal.

---

## Repository Structure

```
localghost/
├── cmd/                      # Daemon entry points
│   ├── scribe/
│   ├── observer/
│   ├── auditor/
│   ├── weaver/
│   ├── sentinel/
│   └── shadow/
├── internal/                 # Shared packages
│   ├── config/
│   ├── crypto/
│   ├── storage/
│   ├── inference/
│   └── dht/                  # The Mist (v3.0+)
├── plugins/                  # Auditor data parsers
├── migrations/               # Postgres schema
├── configs/                  # Per-tier defaults
│   ├── mini.yaml
│   ├── core.yaml
│   ├── pro.yaml
│   └── rack.yaml
├── docker/
├── scripts/
├── docs/
│   └── SECURITY.md           # Security model & duress mode
├── hardware/                 # Bill of materials
└── README.md
```

---

## Hardware Tiers

| Tier | Hardware | Use Case |
|------|----------|----------|
| mini | RPi5 8GB, USB SSD | Journal, basic voice, small models |
| core | ARM64 SBC + NPU, 16GB+ | Full daemon suite, 7-8B models |
| pro | x86/ARM + dedicated GPU | 70B+ models, vision models |
| rack | 1U server, redundant storage | Family/org deployment |

Current target (core tier):

```
> COMPUTE:    ARM64 SBC w/ NPU
> STORAGE:    M.2 NVMe (2280)
> MEMORY:     16GB+ LPDDR4x
> SECURITY:   2× FIDO2 Hardware Keys
> CHASSIS:    Aluminum (Passive Cooling / Fanless)
```

Bill of materials product. Standard parts.

---

## The Mist (P2P Backup) — v3.0

Long-term goal, not a launch feature. For v0.1 through v1.0, use local encrypted backup.

```
┌──────┐     ┌──────┐     ┌──────┐
│ NODE │────▶│ NODE │────▶│ NODE │
└──┬───┘     └──┬───┘     └──┬───┘
   │            │            │
   ▼            ▼            ▼
┌──────┐     ┌──────┐     ┌──────┐
│ NODE │◀────│ NODE │◀────│ NODE │
└──────┘     └──────┘     └──────┘

NO CENTRAL NODE. NO MASTER. JUST THE MESH.
```

How it will work:
1. **Sharding** — Encrypted data split using Reed-Solomon erasure coding
2. **Distribution** — You store shards for others; they store shards for you
3. **Zero-Knowledge** — Shards encrypted before leaving your box
4. **Redundancy** — Only need ~50% of shards to reconstruct

The Pact: Dedicate 20% of your drive to the network. Gain off-site backup for your data.

### Cold Start Reality

| Network Size | Backup Strategy |
|--------------|-----------------|
| Solo | Local-only. Encrypted drive at a friend's house, bank vault, NAS. |
| Small (2-10) | "Friends & Family" mode. Manually add trusted peers. |
| Medium (10-100) | Bootstrap nodes help discovery. |
| Large (100+) | Full DHT. Geographic distribution. |

---

## Economics

| What We Do | What We Cannot Do |
|-----------|------------------|
| Sell pre-built hardware (30% margin) | Sell your data (we don't have it) |
| Sell merch and support | Charge a subscription (no server to cut off) |
| Open-source everything | Force an update (unplug and we vanish) |

When you pay for something once, you own it.  
When you pay for it monthly, it owns you.

---

## The Freehold Directory

We're also building a [registry](https://www.localghost.ai/directory) for the broader local-first ecosystem. Projects that run offline, export data cleanly, and have no kill switch can list themselves by hosting `/.well-known/freehold.json`.

LocalGhost will dogfood this when we have something to certify.

---

## Support Development

**Ethereum:** `zerocool.eth` / `0xc72C85BDd6584324619176618E86E5e3196C6b47`

---

## License

Code: MIT  
Hardware designs: CC BY-SA 4.0

---

## Links

- Website: [localghost.ai](https://www.localghost.ai)
- Manifesto: [localghost.ai/manifesto](https://www.localghost.ai/manifesto)
- GitHub: [github.com/LocalGhostDao](https://github.com/LocalGhostDao)
- Contact: info@localghost.ai

---

```
THE CAGE IS UNLOCKED. THE BARS ARE MADE OF HABIT.
THE EXIT IS OPEN.
```