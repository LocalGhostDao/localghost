# Security Model

```
█▀▀  █▀▀  █▀▀  █░█  █▀█  █  ▀█▀  █▄█
▄▄█  █▄▄  █▄▄  █▄█  █▀▄  █  ░█░  ░█░
```

> *"Privacy is the power to selectively reveal oneself to the world."*  
> [A Cypherpunk's Manifesto](https://www.localghost.ai/cypherpunk), 1993

---

## The Honeypot Problem

LocalGhost creates a searchable, correlated record of your entire life. Journals. Bank transactions. Health metrics. Screen recordings. Location history. Everything `ghost.synthd` has ever connected.

If law enforcement seizes your hardware and compels you to unlock it, encryption alone doesn't help, you've just handed them a perfectly indexed database of your existence.

Encryption protects data at rest. It doesn't protect you from a warrant, a wrench, or a border crossing.

For the longer thinking behind this document, see [The Honeypot Under Your Desk](https://www.localghost.ai/hard-truths/honeypot).

---

## The Multi-PIN Architecture

`ghost.secd` manages two or more encrypted volumes, each with a different derived key. The FIDO2 hardware key proves physical possession but doesn't store the PINs anywhere. `ghost.secd` derives the decryption key from whichever PIN you entered, and the volume that derived key opens is the volume you see.

There is no toggle in the software, no setting that says "duress mode active," nothing for a forensic examiner to find that would suggest other worlds exist. Each volume is a filesystem mounted by a different derived key. Real volumes sit alongside decoy volumes in the same disk layout, and the real volume lives in unallocated space that looks like random noise to anyone without the real PIN.

### How Unlock Works

```
┌──────────────────────┐
│  1. Insert FIDO2 Key │
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  2. Enter PIN        │
│     (into ghost.secd)│
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  3. ghost.secd       │
│     derives key      │
│     (Argon2id)       │
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  4. FIDO2 signs      │
│     challenge        │
│     (presence proof) │
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  5. Volume unlocks   │
│     (whichever one   │
│      this key opens) │
└──────────────────────┘
```

The FIDO2 key doesn't store PINs, it verifies you're physically present. `ghost.secd` decides which volume to decrypt based on which PIN you entered.

### The PIN Types

Every PIN is treated identically by `ghost.secd`. A PIN derives a key, the key opens a volume, and what's inside the volume determines what happens next. There are three kinds of thing a volume can contain.

| Volume contents | What happens |
|-----|--------------|
| Real filesystem | The real system boots. Your actual data. |
| Decoy filesystem | A decoy boots. Either a fresh-looking system or a populated decoy you built. |
| Purge marker | `ghost.secd` recognises the marker and begins destroying everything. |

You configure as many PINs as you want, any combination of real, decoy, and purge. One PIN if you don't want duress mode at all. Two if you want a single decoy. Five if you want layered plausibility where some PINs open populated decoys and others open volumes that look freshly initialised. Any combination that includes a purge PIN that destroys everything instead of unlocking anything useful.

### The Structural Advantage

There is no metadata anywhere on the box recording how many PINs exist or what each one does. The configuration lives inside the encrypted volumes themselves and is invisible without the keys.

An attacker who demands every PIN you have:

- Can't verify they got them all
- Can't distinguish a duress PIN from a real one without typing it
- Can't tell from the outside which PIN unlocks a volume and which one wipes the box

The advantage is ambiguity, not physical impossibility. A sufficiently determined adversary with enough time can compel every PIN you have. What this architecture offers is structural uncertainty about completeness, and that is a real gain against the bored border agent, the curious customs officer, and the wrench attack scenario where you want to be able to cooperate convincingly under coercion.

---

## What v1 Is Built For

`ghost.secd` v1 is built primarily for the bored border agent, the curious customs officer, the routine inspection where someone with legal authority wants to look but isn't running forensics.

v1 is built secondarily for the wrench attack scenario ([XKCD 538](https://xkcd.com/538/)) where you want to be able to cooperate convincingly under coercion.

It is **not** built for a targeted forensic examination by a state-level adversary who knows LocalGhost ships duress PINs and is specifically looking for the hidden volume signature. Hidden volume detection is a known research area, VeraCrypt's design has known statistical fingerprints, and any system that documents duress mode as a feature is by definition known to ship duress mode. If your threat model is a nation-state with prior knowledge and time, you need a different tool, and probably a different country.

At the other end of the coercion scale there is a hard limit. A border agent in an adversarial jurisdiction can compel every PIN you have, someone holding a weapon to your head can compel every PIN you have, and no duress architecture physically prevents you from sharing every PIN when the cost of refusing is high enough.

---

## Decoy Volumes

A decoy volume is just a filesystem mounted by a duress PIN's derived key. What the filesystem contains determines how convincing the decoy is, and there are three approaches that ship at different times.

| Approach | Shipped | How it works |
|-----|-----|-----|
| Fresh-looking system | v1.0 | The decoy volume holds what looks like a freshly initialised LocalGhost install. No history, no data, nothing suspicious. The story is that you just set up the box. |
| Manually populated | v1.0 | You build the decoy yourself. Write a few decoy journals, import a decoy photo library, generate a few months of unremarkable activity. Slow to set up, high fidelity under inspection. |
| Auto-generated | v1.5+ | `ghost.secd` generates a fully convincing decoy life to populate the volume, with believable journals, plausible spending patterns, and a lived-in photo library. Substantially harder and not yet solved. |

### The Decoy Generator Problem

Automatic decoy generation is aspirational. The design has problems that aren't solved, including how to make a generated life look lived in without falling into the uncanny valley where the absence of texture is the tell.

Sanitised data wouldn't work because the patterns themselves are identifying, and any decoy that preserved your real patterns with the dangerous bits redacted would just be a slightly worse version of surrender. The decoy has to be a different boring person who happens to use the same hardware, not a scrubbed version of you.

The duress architecture ships in v1.0. The decoy generator does not, and we don't want to oversell the part of the system that hasn't been built yet.

---

## The Purge

The Purge is just another PIN in the multi-PIN scheme. `ghost.secd` does not distinguish between a real PIN, a duress PIN, and a purge PIN at the architecture level, because every PIN derives a key, every key opens a volume, and the only thing that determines what happens next is what `ghost.secd` finds inside that volume after it mounts.

Most volumes contain a filesystem with your data, or a decoy filesystem. The purge volume is almost empty, holding nothing but a short string that `ghost.secd` recognises as the instruction to start destroying everything.

### What Gets Destroyed

When the purge marker is read:

1. All other encrypted volumes overwritten with random data
2. All daemon databases dropped, `ghost.noted`, `ghost.framed`, `ghost.tallyd`, `ghost.synthd`, `ghost.voiced`, `ghost.shadowd`
3. Postgres instances wiped, embeddings, indexes, everything
4. Redis caches flushed, session state, pub/sub queues
5. Mist shards (if `ghost.mistd` is enabled) dereferenced so distributed copies become unrecoverable
6. Encryption keys destroyed
7. Box reboots to factory state

### The User Experience

The user experience is deliberately undramatic. You type the purge PIN, the volume mounts, `ghost.secd` reads the marker, and the screen shows what looks like a first-run setup wizard, the kind of screen you would see on a brand-new device that has never been configured.

Behind that screen, destruction runs. By the time it finishes, the box is on its way back to factory state with no trace of what was on it before.

The attacker watching you type sees nothing alarming. They see a fresh-looking device that could be a brand-new install, or maybe the wrong PIN, or maybe just a box that was never set up properly. The assumptions they walk away with are not the assumptions they walked in with.

---

## Why Not Just Wipe?

Wipe and duress answer different questions.

The duress PIN answers how to cooperate without surrendering. The Purge answers how to make sure no one ever recovers anything from the box, ever. Those are not the same question, and neither answer is sufficient on its own.

| Scenario | Wipe alone | Multi-PIN with decoy |
|----------|------|---------------|
| Border crossing | Data gone. You pass. Hope you had backups. | Type duress PIN. Show decoy. Pass inspection. Fly home. Real data intact. |
| Wrench attack | Data gone. They may not believe you and keep going. | Type duress PIN. They get "everything." They leave. You still have your life. |
| Targeted forensic | No difference, they know what a wiped box looks like. | Won't help against a state-level adversary with prior knowledge. |
| Actual emergency | Appropriate. Use the purge PIN. | The purge PIN is part of this architecture, not separate from it. |

Decoy volumes buy time and preserve your data. The purge is for when you genuinely need everything gone.

---

## Remembering Several PINs

Remembering several PINs is harder than it sounds and most people do it badly.

One approach is to anchor everything to one number you already know and offset from there. If your real PIN is 2525, then 2524 is one decoy, 2523 is another, and 1525 is the purge. The first three digits stay constant across the decoys with the last digit walking down by one each time. The purge changes a different position entirely so you can never confuse the decoy pattern with the destruction pattern.

Each PIN is distinct enough that you won't confuse them under stress, and related enough that you don't have to memorise four unrelated numbers.

Build whatever mnemonic works for you. Build the system before you set the PINs, because trying to invent a memory aid after the fact is how you lock yourself out of the real volume.

---

## Mobile

iOS and Android biometric APIs don't expose which finger unlocked the device. The secure enclave returns a simple pass/fail, we can't distinguish thumbs.

The mobile app uses the same multi-PIN approach as the hardware. After biometric unlock (for convenience), the app prompts for your LocalGhost PIN. Different PIN, different reality. Same screen, same flow, only you know which world you're entering.

For maximum security, disable biometrics entirely and use PIN-only. Biometrics can be compelled. A PIN in your head cannot be seen.

---

## Technical Implementation

We don't roll our own crypto. The multi-PIN design builds on established, audited tools.

### Volume Layout

```
┌─────────────────────────────────────────────────────────┐
│                    PHYSICAL DISK                        │
├─────────────────────────────────────────────────────────┤
│  LUKS HEADERS (detached)                                │
├─────────────────────────────────────────────────────────┤
│  DECOY VOLUME(S)                  │   UNALLOCATED       │
│  (Duress PIN keys decrypt)        │   (looks random)    │
│                                   │                     │
│  - Decoy filesystem               │   ┌──────────────┐  │
│  - Fresh or populated             │   │ REAL VOLUME  │  │
│                                   │   │ (Real PIN)   │  │
│  PURGE VOLUME                     │   └──────────────┘  │
│  (Purge PIN key decrypts)         │                     │
│  - Almost empty, marker only      │                     │
└───────────────────────────────────┴─────────────────────┘
```

**How it works:**

- LUKS2 with detached headers
- FIDO2 key provides **presence verification** (proves physical possession)
- PIN entered into `ghost.secd`, not stored on FIDO2 key
- `ghost.secd` derives decryption key from PIN using Argon2id
- Different PINs produce different derived keys, which decrypt different volumes
- Real volume lives in "unallocated" space, encrypted with its own derived key
- Decoy volumes see unallocated space as free, won't write there unless completely full
- We reserve sufficient unallocated space during setup based on your storage needs

**Inspired by:** VeraCrypt hidden volumes, dm-crypt/LUKS detached headers, Tails persistent storage.

### What We Use

| Component | Tool | Why |
|-----------|------|-----|
| Disk encryption | LUKS2 | Industry standard, audited, supports detached headers |
| Key derivation | Argon2id | Memory-hard, resists GPU attacks |
| Symmetric encryption | AES-256-GCM | Fast, authenticated, hardware-accelerated |
| Presence verification | FIDO2 key | Proves physical possession, prevents remote attacks |
| Secure wipe | `blkdiscard` + `shred` | TRIM-aware for SSDs |

The contribution we're making with `ghost.secd` is architectural, combining tools that already exist into a system where compliance and surrender stop being the same thing.

### Limitations

- Decoy volumes must have enough "free" space to hide the real volume
- If you fill a decoy volume completely, you risk overwriting real data
- Setup wizard calculates safe boundaries and warns you
- Real volume size is fixed at setup (can't grow into decoy space)

---

## Mobile Technical Notes

- PIN entry handled in-app, not by OS biometric API
- Local SQLCipher database per volume, one database per derived key
- Same app, same UI, different content behind different PINs

---

## Optional Features

- **Auto-purge:** trigger full wipe after N failed unlock attempts (configurable, off by default)
- **Remote purge:** signed message triggers destruction if you're separated from the box
- **Dead man's switch:** if not unlocked within N days, auto-purge (for worst-case scenarios)

---

## Summary

| Feature | Purpose | Implementation |
|---------|---------|----------------|
| Encryption | Protects data at rest | LUKS2 + AES-256-GCM |
| FIDO2 presence | Prevents remote attacks | Hardware key must be physically present |
| Multi-PIN volumes | Protects from compelled access | Separate derived keys per PIN, no external metadata |
| Decoy volumes | Plausible deniability | Fresh-looking or populated filesystems unlocked by duress PINs |
| The Purge | Destruction on demand | A PIN-unlocked volume containing a marker `ghost.secd` acts on |

Privacy is about maintaining control over what you reveal, when, and to whom, even under duress.

---

## Open Questions

The decoy generator is the hardest part of this design and the piece least ready to ship. A few things that remain unresolved.

How convincing does decoy data need to be under sustained adversarial analysis, not just a border check. Sustained analysis has time to spot statistical artefacts that a one-hour inspection doesn't. The current thinking is that decoy data only needs to survive the scenarios we're actually designing for, not all possible ones, but the boundary is fuzzy.

Decoy generation is compute-intensive and full regeneration is probably overkill on low-tier hardware. Incremental generation is likely the answer but it introduces its own patterns (regeneration timestamps) that have to be obfuscated.

The Mist (P2P backup) complicates duress significantly. If the real volume has shards in the network and the decoy volumes do not, that asymmetry is visible to a sophisticated observer who can correlate network traffic. The current plan is that both real and decoy volumes maintain parallel shard sets, but the storage overhead is real.

These get resolved through v0.1, v0.2, v1.0, v1.5 rather than up front. The multi-PIN architecture and basic decoy volumes ship in v1.0. The automatic decoy generator is later.