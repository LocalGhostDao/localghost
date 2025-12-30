```
█░   █▀█  █▀▀  ▄▀█  █░   █▀▀  █░█  █▀█  █▀▀  ▀█▀
█▄▄  █▄█  █▄▄  █▀█  █▄▄  █▄█  █▀█  █▄█  ▄▄█  ░█░
```

# THE ONLY CLOUD IS YOU

> *"Privacy is the power to selectively reveal oneself to the world."*  
> *— [A Cypherpunk's Manifesto](https://www.localghost.ai/cypherpunk), 1993*

Your data. Your hardware. Your ghost.

Read [why we build](https://www.localghost.ai/manifesto).

---

## What Is This?

LocalGhost is a privacy-first, self-hosted AI system that runs entirely on your hardware. No cloud. No subscriptions. No surveillance. Just a black box that works for you.

This is the core repository — everything you need to run LocalGhost on your own machine:

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

## Tech Stack

We're keeping the stack simple and boring. Things we know work.

| Layer | Technology | Notes |
|-------|------------|-------|
| Language | Go | Single binary, no runtime deps, battle-tested, boring, reliable |
| OS | Debian (on our builds) | Stable, minimal, well-understood |
| Database | Postgres + pgvector | Structured data + vector embeddings in one place |
| Cache | Redis | Session state, pub/sub between daemons, job queue |
| Web | Nginx | Reverse proxy, TLS termination, static assets |
| Inference (v0.1) | External APIs or Ollama | Whatever works. Claude, OpenAI, local Ollama. Ship first, optimise later. |
| Inference (v0.2) | Ollama / llama.cpp | Local-first as default |
| Inference (v1.0+) | Direct model loading | No wrappers. Weights in RAM/VRAM via Go bindings. |
| IPC | Unix sockets + Redis pub/sub | Daemons talk locally. No network overhead. |
| Notifications | Direct connection | Your phone connects directly to your box. No push servers, no Firebase, no relay. |

No third-party cloud services required. If you want to use external APIs early on while testing, that's fine. The architecture doesn't lock you in.

---

## The Daemons

Six daemons. Each has one job. All communicate over local Unix sockets.

| Daemon | Role | What It Does |
|--------|------|--------------|
| THE SCRIBE | Text Ingestion | Indexes journals, notes, transcripts. Stores in Postgres with vector embeddings. |
| THE OBSERVER | Vision Pipeline | Processes camera/screen input. OCR, scene tagging, local image embeddings. Opt-in only. |
| THE AUDITOR | Metrics Collector | Imports bank CSVs, screen time logs, git history. Structured data into Postgres. |
| THE WEAVER | Correlation Engine | Runs inference. Correlates timestamps across data sources. Finds patterns via vector similarity. |
| THE SENTINEL | Encryption & Backup | FIDO2 key management, AES-256-GCM encryption. Local backup to USB/NAS/S3-compatible. P2P backup via The Mist comes in v3.0. |
| THE SHADOW | Query Interface | Web UI + API. Ask questions, get answers with citations to your own data. |

### Daily Digest

The Weaver runs background analysis overnight and identifies interesting patterns in your data. Notifications go directly to your phone — your phone connects to your LocalGhost box over your local network or via VPN when you're away. No notification relay servers, no Firebase, no third parties. Just a direct connection between your devices.

---

## Repository Structure

```
localghost/
├── cmd/                      # Daemon entry points
│   ├── scribe/               # Text ingestion
│   ├── observer/             # Vision pipeline
│   ├── auditor/              # Metrics collector
│   ├── weaver/               # Correlation engine
│   ├── sentinel/             # Encryption & backup
│   └── shadow/               # Query interface
├── internal/                 # Shared packages
│   ├── config/               # Configuration (YAML)
│   ├── crypto/               # Encryption, FIDO2
│   ├── storage/              # Postgres + Redis
│   ├── inference/            # Model interface (external → Ollama → direct)
│   └── dht/                  # The Mist (v3.0+)
├── migrations/               # Postgres schema
├── configs/                  # Per-tier defaults
│   ├── mini.yaml
│   ├── core.yaml
│   ├── pro.yaml
│   └── rack.yaml
├── docker/                   # Compose files per tier
├── scripts/                  # install.sh, upgrade.sh, backup.sh
├── docs/                     # Architecture, security, API
├── hardware/                 # Bill of materials
├── SUPPORTERS.md
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

All tiers run the same binaries. Config files tune resource limits, model selection, and enabled features.

---

## Releases

Software and hardware are versioned separately.

### Software Releases

Named after ghosts, smallest to largest. Features ship incrementally.

| Version | Codename | Focus | Target |
|---------|----------|-------|--------|
| v0.1 | Wisp | MVP: Scribe + Weaver + Shadow, external/Ollama inference, local backup | Month 3 |
| v0.2 | Shade | Multimodal: Observer + Auditor, local-first inference, daily digest | Month 6 |
| v0.3 | Specter | Hardware: Official kits ship, one-click install | Month 9 |
| v1.0 | Phantom | Production: Stable APIs, direct model loading, security audit | Month 12 |
| v2.0 | Wraith | Scale: Multi-user, rack support | Month 15+ |
| v3.0 | Poltergeist | The Mist: P2P backup, DHT | Month 18+ |

### Hardware Tiers

Run any software version on any tier.

| Tier | Hardware | Use Case |
|------|----------|----------|
| mini | RPi5 8GB, USB SSD, USB mic | Journal, basic voice, small models (Phi-3, Gemma 2B) |
| core | ARM64 SBC + NPU, 16GB+, NVMe | Full daemon suite, 7-8B models |
| pro | x86/ARM + dedicated GPU | 70B+ models, vision models |
| rack | 1U server, redundant storage, IPMI | Family/org deployment, multiple users |

Example: Run `wisp` (v0.1) on a `core` box today, upgrade to `shade` (v0.2) next month. Same hardware, new software.

---

## Pricing & Support

Everything is free. The software doesn't call home. We can't stop you from using it. We wouldn't want to.

But if LocalGhost is useful to you, you can support development and get something back.

### Hardware Tiers

| | mini | core | pro | rack |
|---|------|------|-----|------|
| Software | Full | Full | Full | Full |
| Community support | ✓ | ✓ | ✓ | ✓ |
| Air-gap kit | ✓ | ✓ | ✓ | ✓ |
| Name in SUPPORTERS.md | — | — | ✓ | ✓ |
| Priority support | — | — | — | ✓ |
| Feature input | — | — | — | ✓ |
| Setup assistance | — | — | — | ✓ |

Air-gap kit = Ethernet port blockers + USB data blockers. Included free with every build. We don't charge for security.

How it works:
- Download and run any tier — free forever
- Buy a pre-built `pro` or `rack` box — you're a supporter, name goes in the file
- Buy `rack` — we help you set it up and provide priority support
- We can't enforce any of this. If you use it commercially and find it valuable, pay what it's worth.

### Donations

Don't need hardware? You can still support the mission.

| Amount | What You Get |
|--------|--------------|
| £50+ | Name in SUPPORTERS.md |
| £200+ | Above + "Founding Ghost" badge |
| £500+ | Above + dev call invite |
| £1000+ | Above + one year priority support + feature input |

One-time payments. No subscriptions. You pay once, you get something.

All donations go to development. No VC. No strings. Just code and hardware.

Donate: [localghost.ai](https://localghost.ai/#economics) or send ETH to `zerocool.eth`

Merch coming eventually. See [MERCH.md](MERCH.md) when it exists.

---

## Current Hardware Target (core tier)

Subject to change as we test configurations:

```
> COMPUTE:    ARM64 SBC w/ NPU
> STORAGE:    M.2 NVMe (2280)
> MEMORY:     16GB+ LPDDR4x
> SECURITY:   2× FIDO2 Hardware Keys
> CHASSIS:    Aluminum (Passive Cooling / Fanless)
```

Bill of materials product. Standard parts you can source yourself.

---

## The Mist (P2P Backup) — v3.0

P2P backup is a long-term goal, not a launch feature. For v0.1 through v1.0, use local encrypted backup: USB, NAS, or S3-compatible storage (R2, Backblaze, MinIO). This covers 99% of use cases.

The Mist becomes relevant when we have enough users to form a resilient network. That takes time.

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

1. SHARDING — Your encrypted data is split into pieces using Reed-Solomon erasure coding
2. DISTRIBUTION — You store shards for others; they store shards for you
3. ZERO-KNOWLEDGE — Shards are encrypted before leaving your box. Hosts can't read them.
4. REDUNDANCY — You only need ~50% of shards to reconstruct (tunable)

The Pact: Dedicate 20% of your drive to the network. Gain off-site backup for your data.

### Cold Start Reality

P2P backup requires peers. If you're the only LocalGhost user in your area, The Mist doesn't help.

| Network Size | Backup Strategy |
|--------------|-----------------|
| Solo (1 node) | Local-only. Encrypted drive at a friend's house, bank vault, NAS. |
| Small (2-10 nodes) | "Friends & Family" mode. Manually add trusted peers by IP/pubkey. |
| Medium (10-100 nodes) | Bootstrap nodes help discovery. Shards spread across the network. |
| Large (100+ nodes) | Full DHT. Geographic distribution. Resilient to churn. |

The Mist is opt-in and disabled by default. Local backup always works.

Implementation will live in `internal/dht/`. Kademlia-style routing, QUIC transport, NAT traversal.

---

## Development Roadmap

This is an open-source project. we ship continuously. Tags mark stable releases.

---

### Phase 1: "Bones" — Months 1-3 → `wisp` (v0.1)

Goal: Write notes, ask questions about your own data.

Included:
- The Scribe — text ingestion, journaling
- The Weaver — RAG pipeline (pgvector + inference)
- The Shadow — web UI for queries
- Basic encryption (FIDO2 key unlock)
- Local backup to USB/NAS

Not included: The Mist (P2P), The Observer (vision), hardware sales.

Tech: External APIs (Claude, OpenAI) or Ollama for inference. Whatever works. Postgres + pgvector for embeddings. Redis for job queue.

---

### Phase 2: "Senses" — Months 4-6 → `shade` (v0.2)

Goal: Multimodal inputs. See more than just text.

Included:
- The Auditor — bank CSVs, screen time, git history, health exports
- The Observer — camera/screen input, OCR, scene tagging (opt-in)
- Cross-source correlation
- Daily digest notifications (direct to phone)
- Local-first inference (Ollama/llama.cpp as default)
- S3-compatible backup (R2, Backblaze, MinIO)

---

### Phase 3: "Shell" — Months 6-9 → `specter` (v0.3)

Goal: Hardware ships only after software is stable.

Included:
- Official `mini` and `core` reference designs
- Pre-built images for supported boards
- One-click installer
- Hardware validation test suite
- SUPPORTERS.md for early buyers

Why wait: Shipping broken hardware kills trust. Software-first means we can iterate without bricking anyone's box.

---

### Phase 4: "Form" — Months 9-12 → `phantom` (v1.0)

Goal: Production-ready. Stable. Documented. Supportable.

Included:
- The Sentinel — full key management, encrypted shards
- Direct model loading (drop Ollama dependency)
- `pro` and `rack` hardware tiers
- Priority support infrastructure
- API stability guarantees
- Security audit

Backup: Local + NAS + S3-compatible. Encrypted at rest.

---

### Phase 5: "Mist" — Month 18+ → `poltergeist` (v3.0)

Goal: P2P backup for those who want it.

Included:
- The Mist — DHT, shard distribution, Friends & Family mode
- Bootstrap node network
- NAT traversal, QUIC transport
- Geographic redundancy

P2P requires critical mass. Local backup solves the problem for years. The Mist is a long-term goal.

---

### Timeline

| Milestone | Target | What's Usable |
|-----------|--------|---------------|
| First commit | Week 1 | Nothing (watch the repo) |
| wisp-alpha | Month 2 | Text notes + basic RAG |
| wisp (v0.1) | Month 3 | Full Scribe/Weaver/Shadow |
| shade (v0.2) | Month 6 | Multimodal, daily digest |
| specter (v0.3) | Month 9 | Hardware kits ship |
| phantom (v1.0) | Month 12 | Production-ready |
| wraith (v2.0) | Month 15+ | Multi-user, rack support |
| poltergeist (v3.0) | Month 18+ | P2P backup |

Star the repo. Watch the commits. Jump in when it's useful to you.

---

## Getting Started

Not implemented yet. When it is:

```bash
git clone https://github.com/LocalGhostDao/localghost.git
cd localghost

export GHOST_TIER=core  # mini | core | pro | rack

./scripts/install.sh
```

The installer will:
1. Check hardware compatibility
2. Pull required model weights
3. Configure Docker containers
4. Generate encryption keys (requires your FIDO2 key)
5. Start all daemons
6. Open the Shadow interface at `http://ghost.local`

Watch this repo for updates.

---

## Development

```bash
make build          # Build all daemons
make test           # Run tests
make build-scribe   # Build specific daemon
make lint           # Lint
```

Written in Go. Standard library preferred. Boring and reliable over clever.

---

## Philosophy

We make money selling convenience (pre-built hardware), merch, and support (setup assistance, priority response for businesses running rack deployments). The software is free. All of it. Forever.

| What We Do | What We Cannot Do |
|-----------|------------------|
| Sell pre-built hardware (30% margin) | Sell your data (we don't have it) |
| Sell merch and support | Charge a subscription (no server to cut off) |
| Open-source everything | Force an update (unplug and we vanish) |
| High-five DIY builders | Train AI on your life (we can't see it) |

When you pay for something once, you own it.
When you pay for it monthly, it owns you.

### Pragmatism

The [manifesto](https://www.localghost.ai/manifesto) describes where we're going, not where we start.

Day 1 won't be perfect. We'll ship with external API support before local inference is optimised. We'll have basic backup before The Mist exists. Some features will be rough. Some won't exist yet.

That's fine. We'd rather ship something useful today than wait for perfection. The architecture is sound. The direction is clear. We'll get there by shipping, not by planning.

If you want the full vision on day 1, wait for v3.0. If you want to help build it, jump in now.

---

## Security Model

- Air-gap capable — works fully offline
- FIDO2 encryption — your hardware keys unlock your data
- No phone-home — zero telemetry, zero analytics, zero beacons
- Auditable — all code is open source
- Architecture is the defense — we can't access what we can't see

If law enforcement comes with a warrant, compliance looks like:
*"We have no data. We have no logs. We don't know who uses the hardware. Here's the open-source code. Good luck."*

---

## Contributing

Contributions welcome. The codebase is Go.

We prefer:
- Standard library over external deps
- Explicit over clever
- Boring and reliable over exciting and fragile

See [CONTRIBUTING.md](CONTRIBUTING.md) when it exists.

---

## License

Code: MIT — Do whatever you want.

Hardware designs: CC BY-SA 4.0

---

## Links

- Website: [localghost.ai](https://localghost.ai)
- Manifesto: [localghost.ai/manifesto](https://localghost.ai/manifesto)
- GitHub: [github.com/LocalGhostDao](https://github.com/LocalGhostDao)
- Contact: info@localghost.ai

---

```
THE CAGE IS UNLOCKED. THE BARS ARE MADE OF HABIT.
YOU ARE WAITING FOR A PERMISSION SLIP THAT WILL NEVER COME.

THE EXIT IS OPEN.
```
