```
█░   █▀█  █▀▀  ▄▀█  █░   █▀▀  █░█  █▀█  █▀▀  ▀█▀
█▄▄  █▄█  █▄▄  █▀█  █▄▄  █▄█  █▀█  █▄█  ▄▄█  ░█░
```

# THE ONLY CLOUD IS YOU

> *"Privacy is the power to selectively reveal oneself to the world."*  
> *— A Cypherpunk's Manifesto, 1993*

**Your data. Your hardware. Your ghost.**

---

## What Is This?

LocalGhost is a privacy-first, self-hosted AI system that runs entirely on your hardware. No cloud. No subscriptions. No surveillance. Just a black box that works for you.

This is the **core repository** — everything you need to run LocalGhost on your own machine:

- Hardware specifications and bill of materials
- Daemon source code (the six agents that power the system)
- Docker configurations for one-click deployment  
- Installation scripts and upgrade tooling
- Documentation and architecture decisions

The [website](https://github.com/LocalGhostDao/web) lives in a separate repo.

---

## The Architecture

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

| Layer | Technology | Why |
|-------|------------|-----|
| **Language** | Go | Single binary deploys, no runtime deps, boring and reliable |
| **OS** | Debian (our builds) | Stable, minimal, well-understood |
| **Database** | Postgres + pgvector | Structured data + vector embeddings in one place |
| **Cache** | Redis | Session state, pub/sub between daemons, queue for background jobs |
| **Web** | Nginx | Reverse proxy, TLS termination, static assets |
| **Inference** | Direct model loading | No Ollama, no wrappers. Model weights loaded directly into RAM/VRAM via Go bindings. Fastest path. |
| **IPC** | Unix sockets + Redis pub/sub | Daemons talk locally. No network overhead. |
| **Notifications** | Self-hosted push | ntfy, Matrix, or local WebSocket. No Firebase, no APNs relay. |

**No middleware. No abstraction layers.** Your query goes from The Shadow → directly to model weights in memory → back to you. The only thing between you and inference is your own hardware.

---

## The Fleet: Local Daemons

Six daemons. Each has a single job. All communicate over local Unix sockets.

| Daemon | Technical Role | What It Does |
|--------|----------------|--------------|
| **THE SCRIBE** | Text Ingestion | Indexes journals, notes, transcripts. Stores in Postgres with vector embeddings. |
| **THE OBSERVER** | Vision Pipeline | Processes camera/screen input. OCR, scene tagging, local image embeddings. Opt-in only. |
| **THE AUDITOR** | Metrics Collector | Imports bank CSVs, screen time logs, git history. Structured data into Postgres. |
| **THE WEAVER** | Correlation Engine | Runs local LLM inference. Correlates timestamps across data sources. Finds patterns via vector similarity. |
| **THE SENTINEL** | Encryption & Backup | FIDO2 key management, AES-256-GCM encryption, shard distribution to The Mist. |
| **THE SHADOW** | Query Interface | Web UI + API. Ask questions, get answers with citations to your own data. |

**Daily Digest:** The Weaver runs background analysis overnight and pushes notifications about interesting patterns — directly to your phone via self-hosted push (ntfy/Matrix) or local network. No third-party notification services.

---

## Repository Structure

```
localghost/
├── cmd/                      # Daemon entry points
│   ├── scribe/               # Text ingestion daemon
│   ├── observer/             # Vision pipeline daemon
│   ├── auditor/              # Metrics collector daemon
│   ├── weaver/               # Correlation engine daemon
│   ├── sentinel/             # Encryption & backup daemon
│   └── shadow/               # Query interface daemon
├── internal/                 # Shared internal packages
│   ├── config/               # Configuration loading (YAML)
│   ├── crypto/               # Encryption, FIDO2, key management
│   ├── storage/              # Postgres + Redis interfaces
│   ├── inference/            # Direct model loading, inference runtime
│   └── dht/                  # The Mist: Kademlia DHT for P2P backup
├── migrations/               # Postgres schema migrations
├── configs/                  # Default configs per hardware tier
│   ├── mini.yaml
│   ├── core.yaml
│   ├── pro.yaml
│   └── rack.yaml
├── docker/                   # Compose files per tier
├── scripts/                  # install.sh, upgrade.sh, backup.sh
├── docs/                     # Architecture, security, API
├── hardware/                 # Bill of materials (may move to wiki)
├── SUPPORTERS.md             # The people who make this possible
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

All tiers run the same daemon binaries. Config files tune resource limits, model selection, and enabled features per tier.

---

## Releases

Software and hardware are versioned separately.

### Software Releases

Software versions are named after ghosts, smallest to largest.

| Version | Codename | Status |
|---------|----------|--------|
| v0.1 | **Wisp** | `PLANNED` — First breath |
| v0.2 | **Shade** | Starting to take form |
| v0.3 | **Specter** | Gaining presence |
| v1.0 | **Phantom** | Fully formed |
| v2.0 | **Wraith** | Powerful |
| v3.0+ | **TBD** | Poltergeist, Revenant, Banshee... |

### Hardware Reference Designs

Hardware tiers are independent of software version. Run any software version on any tier.

| Tier | Hardware | Use Case |
|------|----------|----------|
| **mini** | RPi5 8GB, USB SSD, USB mic | Journal, basic voice, small models (Phi-3, Gemma 2B) |
| **core** | ARM64 SBC + NPU, 16GB+, NVMe | Full daemon suite, 7-8B models, real-time inference |
| **pro** | x86/ARM + dedicated GPU | 70B+ models, vision models, parallel inference |
| **rack** | 1U server, redundant storage, IPMI | Family/org deployment, multiple users |

**Example:** You might run `wisp` (v0.1) on a `core` box today, then upgrade to `shade` (v0.2) next month. Same hardware, new software.

---

## Pricing & Support

**Everything is free.** The software doesn't call home. We can't stop you. We wouldn't want to.

But if LocalGhost is useful to you, you can support development — and get something back.

### Hardware Tiers

| | **mini** | **core** | **pro** | **rack** |
|---|----------|----------|---------|----------|
| **Software** | Full | Full | Full | Full |
| **Community support** | ✓ | ✓ | ✓ | ✓ |
| **Air-gap kit** | ✓ | ✓ | ✓ | ✓ |
| **Name in SUPPORTERS.md** | — | — | ✓ | ✓ |
| **Priority support** | — | — | — | ✓ |
| **Feature input** | — | — | — | ✓ |
| **Setup assistance** | — | — | — | ✓ |

**Air-gap kit** = Ethernet port blockers + USB data blockers. Included free with every build. We don't charge for security.

**How it works:**
- Download and run any tier — free forever, no strings
- Buy a pre-built `pro` or `rack` box — you're a supporter, name goes in the file
- Buy `rack` — we help you set it up and provide priority support
- We can't enforce any of this. If you use it commercially and find it valuable, pay what it's worth.

### Donation Tiers

Don't need hardware? You can still support the mission.

| Amount | What You Get |
|--------|--------------|
| £50+ | Name in `SUPPORTERS.md` |
| £200+ | Above + "Founding Ghost" badge |
| £500+ | Above + dev call invite |
| £1000+ | Above + one year priority support + feature input |

One-time payments. No subscriptions. You pay once, you get something.

All donations go to development. No VC. No strings. Just atoms and code.

**Donate:** [localghost.ai](https://localghost.ai/#economics) or send ETH to `zerocool.eth`

*Merch coming eventually. See [MERCH.md](MERCH.md) when it exists.*

---

## Current Hardware Target (core tier)

From the website — subject to change:

```
> COMPUTE:    ARM64 SBC w/ NPU
> STORAGE:    M.2 NVMe (Standard 2280 Form Factor)  
> MEMORY:     16GB+ LPDDR4x
> SECURITY:   2× FIDO2 Hardware Keys
> CHASSIS:    Aluminum (Passive Cooling / Fanless)
```

This is a "Bill of Materials" product. Standard parts you could buy yourself. Final specs published before launch.

---

## The Mist (P2P Backup)

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

**How it works:**

1. **SHARDING** — Your encrypted data is split into thousands of pieces using Reed-Solomon erasure coding
2. **DISTRIBUTION** — You store shards for others; they store shards for you
3. **ZERO-KNOWLEDGE** — Shards are encrypted before leaving your box. Hosts can't read them.
4. **REDUNDANCY** — You only need ~50% of shards to reconstruct (tunable)

**The Pact:** Dedicate 20% of your drive to the network. Gain off-site backup for your data.

### The Cold Start Problem (Honest)

P2P backup requires peers. If you're the only LocalGhost user in your area, The Mist doesn't help you.

**Our approach:**

| Network Size | Backup Strategy |
|--------------|-----------------|
| **Solo (1 node)** | Local-only. Use standard off-site backup (encrypted drive at a friend's house, bank vault). |
| **Small (2-10 nodes)** | "Friends & Family" mode. Manually add trusted peers by IP/pubkey. You know who holds your shards. |
| **Medium (10-100 nodes)** | Bootstrap nodes help discovery. Shards spread across the network. |
| **Large (100+ nodes)** | Full DHT. Geographic distribution. Resilient to churn. |

**The Mist is opt-in and disabled by default.** Your data works fine without it. Local backup to USB/NAS is always supported.

Implementation lives in `internal/dht/`. Kademlia-style routing, QUIC transport, NAT traversal via STUN/relay fallback.

---

## Roadmap

### Phase 1: Foundation
- [ ] Repository structure and build tooling
- [ ] Core daemon interfaces and IPC protocol
- [ ] Configuration management
- [ ] Basic encryption layer (FIDO2 integration)

### Phase 2: Input Daemons
- [ ] The Scribe — text capture, journaling, voice transcription
- [ ] The Auditor — metrics ingestion (screen time, git, bank exports)
- [ ] The Observer — vision pipeline (opt-in)

### Phase 3: Synthesis
- [ ] The Weaver — local LLM inference, pattern recognition
- [ ] Cross-daemon correlation engine
- [ ] Goal tracking and accountability

### Phase 4: Output & Security
- [ ] The Shadow — user interface, honest answers
- [ ] The Sentinel — encryption, sharding, key management
- [ ] The Mist — DHT integration, P2P backup

### Phase 5: Distribution
- [ ] One-click installer per release tier
- [ ] Docker Compose orchestration
- [ ] Hardware validation scripts
- [ ] Pre-built images for common boards

---

## Getting Started (Future)

```bash
# Clone
git clone https://github.com/LocalGhostDao/localghost.git
cd localghost

# Choose your hardware tier
export GHOST_TIER=core  # mini | core | pro | rack

# One-click install
./scripts/install.sh
```

The installer will:
1. Check hardware compatibility
2. Pull required model weights
3. Configure Docker containers
4. Generate encryption keys (requires your FIDO2 key)
5. Start all daemons
6. Open the Shadow interface at `http://ghost.local`

**Not implemented yet.** Watch this repo for updates.

---

## Development

```bash
# Build all daemons
make build

# Run tests
make test

# Build specific daemon
make build-scribe

# Lint
make lint
```

Written in Go. Standard library preferred. Boring and reliable over clever.

---

## Philosophy

We make money selling atoms (hardware) and optional software upgrades. We will never sell your data because we literally cannot access it.

| What We Do | What We Cannot Do |
|------------|-------------------|
| Sell you hardware (30% margin) | Sell your data (we don't have it) |
| Sell optional upgrades | Charge a subscription (no server to cut off) |
| Open-source everything | Force an update (unplug and we vanish) |
| High-five DIY builders | Train AI on your life (we can't see it) |

When you pay for something once, you own it.  
When you pay for it monthly, it owns you.

---

## Security Model

- **Air-gap capable** — Works fully offline
- **FIDO2 encryption** — Your hardware keys unlock your data
- **No phone-home** — Zero telemetry, zero analytics, zero beacons
- **Auditable** — All code is open source
- **Architecture is the defense** — We can't access what we can't see

If law enforcement comes with a warrant, compliance looks like:  
*"We have no data. We have no logs. We don't know who uses our hardware. Here's our open-source code. Good luck."*

---

## Contributing

We welcome contributions. The codebase will be Go.

We prefer:
- Standard library over external deps
- Explicit over clever  
- Boring and reliable over exciting and fragile

See [CONTRIBUTING.md](CONTRIBUTING.md) when it exists.

---

## License

**Code:** MIT — Do whatever you want. No strings.

**Hardware designs:** CC BY-SA 4.0

---

## Links

- **Website:** [localghost.ai](https://localghost.ai)
- **Manifesto:** [localghost.ai/manifesto](https://localghost.ai/manifesto)
- **GitHub:** [github.com/LocalGhostDao](https://github.com/LocalGhostDao)
- **Contact:** info@localghost.ai

---

```
THE CAGE IS UNLOCKED. THE BARS ARE MADE OF HABIT.
YOU ARE WAITING FOR A PERMISSION SLIP THAT WILL NEVER COME.

THE EXIT IS OPEN.
```
