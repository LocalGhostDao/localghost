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

## The Fleet: Local Daemons

Six specialized agents. Each has a single job. All run locally.

| Daemon | Role | What It Does |
|--------|------|--------------|
| **THE SCRIBE** | Text & Context | Reads your journals. Maps your syntax. Preserves raw thoughts you self-censor. |
| **THE OBSERVER** | Vision | Sees the bags under your eyes. Tracks the clutter. The only camera that works for you. |
| **THE AUDITOR** | Hard Metrics | Bank logs. Screen time. Commit history. The undeniable baseline. |
| **THE WEAVER** | Synthesis | Connects spending to sadness. Code to sleep. Builds a model of your psyche. |
| **THE SENTINEL** | Security | Encrypts & shards your data. Distributes to the P2P mesh. The deadman switch. |
| **THE SHADOW** | Interface | Brutally honest. If you lie to it, it cites the Auditor to correct you. |

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
├── internal/                 # Shared internal packages
│   ├── config/
│   ├── crypto/
│   ├── storage/
│   └── dht/
├── releases/                 # Version configurations
│   └── wisp/                 # v1 release
│       ├── mini/
│       ├── core/
│       ├── pro/
│       └── rack/
├── hardware/                 # Bill of materials per tier
│   ├── mini.md
│   ├── core.md
│   ├── pro.md
│   └── rack.md
├── docker/                   # Compose files per release/tier
│   └── wisp/
│       ├── mini/
│       ├── core/
│       ├── pro/
│       └── rack/
├── scripts/                  # install.sh, upgrade.sh, backup.sh
├── docs/                     # Architecture, security, API
├── SUPPORTERS.md             # The people who make this possible
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

All tiers within a release share the same daemon code. Only resource allocations and Docker configs differ between tiers.

---

## Releases

Releases are named after ghosts, smallest to largest. Each release contains four hardware tiers.

### Release Codenames

| Version | Codename | Meaning |
|---------|----------|---------|
| v1 | **Wisp** | Barely visible. First breath. |
| v2 | **Shade** | Starting to take form. |
| v3 | **Specter** | Gaining presence. |
| v4 | **Phantom** | Fully formed. |
| v5 | **Wraith** | Powerful. |
| v6+ | **TBD** | Poltergeist, Revenant, Banshee... |

Current release: `wisp` (v1) — **PLANNED**

### Hardware Tiers

| Tier | Hardware | Use Case |
|------|----------|----------|
| **mini** | RPi5 8GB, USB SSD, USB mic | Journal, basic voice |
| **core** | ARM64 SBC + NPU, 16GB+, NVMe | Full daemon suite |
| **pro** | x86/ARM + multi-GPU | Heavy inference, vision |
| **rack** | 1U server, redundant storage | Family/org deployment |

So your first install might be `wisp-core` or `wisp-mini`.

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

## Current Hardware Target (wisp-core)

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

1. **SHARDING** — Your encrypted data is broken into thousands of meaningless pieces
2. **DISTRIBUTION** — You store shards for others; they store shards for you  
3. **ZERO-KNOWLEDGE** — They can't read yours. You can't read theirs.

**The Pact:** Dedicate 20% of your drive to the network. Gain immortality for your data.

Uses Reed-Solomon erasure coding (similar to Ethereum's PeerDAS). You only need ~50% of shards to reconstruct. Kademlia-style DHT for discovery.

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

# Choose your release and tier
export GHOST_RELEASE=wisp
export GHOST_TIER=core

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
