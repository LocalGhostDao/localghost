# Security Model

> *"Privacy is the power to selectively reveal oneself to the world."*  
> — A Cypherpunk's Manifesto, 1993

---

## The Honeypot Problem

LocalGhost creates a searchable, correlated record of your entire life. Journals. Bank transactions. Health metrics. Screen recordings. Location history. Everything the Weaver has ever connected.

If law enforcement seizes your hardware and compels you to unlock it, encryption alone doesn't help — you've just handed them a perfectly indexed database of your existence.

Encryption protects data at rest. It doesn't protect you from a warrant, a wrench, or a border crossing.

---

## Duress Mode

The Sentinel supports two unlock paths — same key, different PIN:

| PIN | What Happens |
|-----|--------------|
| **Real PIN** | Full system. Your actual data. |
| **Duress PIN** | Shadow system. Plausible decoy data. No evidence the real system exists. |

During initial setup, you configure both PINs on your FIDO2 key. They should be different enough that you'll never mix them up — but to someone watching you type, they can't tell which one you entered.

**Example:**
- Real PIN: `8472`
- Duress PIN: `0000`

You'll never accidentally type `0000` when you meant `8472`. But under coercion, you can "cooperate" and enter the duress PIN.

### Unlock Flow

```
┌─────────────────────────┐
│   INSERT FIDO2 KEY      │
│   ENTER PIN             │
└───────────┬─────────────┘
            │
            ▼
    ┌───────────────┐
    │  Which PIN?   │
    └───────┬───────┘
            │
   ┌────────┼────────┐
   ▼        ▼        ▼
┌──────┐ ┌──────┐ ┌──────┐
│ 8472 │ │ 0000 │ │ 9999 │
│ Real │ │Duress│ │Purge │
└──┬───┘ └──┬───┘ └──┬───┘
   │        │        │
   ▼        ▼        ▼
┌──────┐ ┌──────┐ ┌──────┐
│ Real │ │Shadow│ │ WIPE │
│ Data │ │ Data │ │ ALL  │
└──────┘ └──────┘ └──────┘
```

---

## The Shadow System

*This is aspirational. The shadow system is planned for v1.0+, not the initial release.*

This isn't sanitization. It's transformation.

The shadow system doesn't preserve your patterns with sensitive bits removed — patterns themselves are identifying. Instead, The Observer generates a completely different but equally believable life.

### The Principle

| Real You | Shadow You |
|----------|------------|
| Journals about your startup struggles | Journals about hobbies, weather, mild complaints |
| Transactions showing your spending habits | Different spending patterns, different merchants |
| Photos of your actual life | Stock-ish photos, generic moments |
| Location patterns revealing your routine | Different routine, different places |
| Health data showing your conditions | Healthy-normal baseline data |

The shadow isn't you with secrets removed. It's a different boring person who happens to use the same device.

### How It Works

The Observer sees everything. Once daily, it spends compute time generating shadow data:

```
┌─────────────────────────────────────────────────────────┐
│                    THE OBSERVER                         │
│                   (daily job)                           │
│                                                         │
│   Real data ──▶ Analyze patterns ──▶ Generate inverse   │
│                                                         │
│   Your routine?    ──▶  Different routine               │
│   Your writing style? ──▶  Blander style                │
│   Your spending?   ──▶  Different spending              │
│   Your locations?  ──▶  Different locations             │
│                                                         │
│                      ▼                                  │
│              SHADOW VOLUME                              │
│         (believable stranger)                           │
└─────────────────────────────────────────────────────────┘
```

### What Gets Generated

| Data Type | Shadow Version |
|-----------|----------------|
| Journals | Bland entries about nothing — weather, errands, "had a good day" |
| Bank data | Random patterns, generic merchants, unremarkable amounts |
| Photos | Pulled from safe pool or skipped — "sync issues" |
| Location | Randomized routine, avoids your real places |
| Observer | Disabled in shadow ("I turned that off for privacy") |
| Health | Boring baseline — normal sleep, normal activity |

### Covering Gaps

Not everything will have a shadow equivalent. Missing data blames sync issues. Inconsistencies blame beta software. The goal isn't perfection — it's *plausible enough* that discrepancies look like bugs, not deception.

**The best lie is the one that looks most like the truth.** And the truth is: software has bugs, syncs fail, and most people's lives are boring.

**There is no evidence the real system exists.** The shadow system uses all visible disk space. Forensic analysis shows a normal encrypted volume with a consistent, unremarkable person. The real volume is hidden within unallocated space — indistinguishable from random noise to anyone without the real PIN.

---

## Why Not Wipe?

Wiping is irreversible. It's the nuclear option.

| Scenario | Wipe | Hidden Volume |
|----------|------|---------------|
| Border crossing | Data gone. You pass. Hope you had backups. | Show shadow. Pass inspection. Fly home. Real data intact. |
| Wrench attack | Data gone. They might not believe you and keep going. | Show shadow. They get "everything." They leave. You still have your life. |
| Actual emergency | Appropriate. | Won't help — they'll find the shadow system. |

Hidden volumes buy you time and preserve your data. Wipe is for when you genuinely need everything gone.

**Both options exist.** Duress PIN shows the shadow. A separate deliberate action (long-press + PIN + confirmation) triggers actual destruction.

---

## Mobile: Duress PIN

iOS and Android biometric APIs don't expose which finger unlocked the device — the secure enclave returns a simple pass/fail. We can't distinguish thumbs.

Instead, the mobile app uses the same PIN-based approach as the hardware:

| PIN | What Happens |
|-----|--------------|
| **Real PIN** | Full system. Your actual data. |
| **Duress PIN** | Shadow system. Plausible decoy. |

After biometric unlock (for convenience), the app prompts for your LocalGhost PIN. Different PIN, different reality. Same screen, same flow — only you know which world you're entering.

For maximum security, disable biometrics entirely and use PIN-only. Biometrics can be compelled; a PIN in your head cannot be seen.

---

## The Purge (Manual Wipe)

When you genuinely need everything gone — not hidden, *gone* — the Sentinel provides a separate destruction sequence. This is deliberate, not accidental.

**Trigger:** Long-press power (3 sec) + Purge PIN + confirmation prompt

When triggered:
1. All encrypted volumes overwritten with random data
2. All daemon data destroyed: Scribe journals, Observer recordings, Auditor imports, Weaver correlations
3. Shadow system destroyed too — nothing remains
4. Postgres dropped and overwritten — embeddings, indexes, everything
5. Redis flushed — session state, job queues, cache
6. The Mist shards (if enabled) dereferenced — your data becomes unrecoverable from the network
7. Encryption keys destroyed
8. Box reboots to factory state

This is irreversible. Use it when you need to.

---

## Why This Matters

That power means nothing if it can be taken from you at gunpoint. The right to remain silent doesn't help when they can compel your fingerprint.

Duress mode ensures that compliance and cooperation are not the same as surrender. You can hand over a PIN. You can unlock your device. You can let them browse. And they will find exactly what you want them to find — a boring person with nothing to hide.

Your real life stays hidden. Not encrypted. Not wiped. *Hidden.* Indistinguishable from empty space.

We are not building a vault. We are building a room with a false wall.

---

## Technical Implementation

We don't roll our own crypto. The hidden volume design builds on established, audited tools:

### Volume Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    PHYSICAL DISK                        │
├─────────────────────────────────────────────────────────┤
│  LUKS HEADER (detached, stored on FIDO2 key)           │
├─────────────────────────────────────────────────────────┤
│  SHADOW VOLUME                    │   UNALLOCATED      │
│  (Duress PIN decrypts this)       │   (looks random)   │
│                                   │                    │
│  - Decoy Postgres                 │   ┌──────────────┐ │
│  - Decoy Redis                    │   │ REAL VOLUME  │ │
│  - Decoy daemon data              │   │ (Real PIN)   │ │
│                                   │   └──────────────┘ │
└───────────────────────────────────┴────────────────────┘
```

**How it works:**
- LUKS2 with detached headers stored on FIDO2 secure element
- Shadow volume is a normal encrypted filesystem
- Real volume lives in "unallocated" space, encrypted with different key
- Shadow volume's filesystem sees unallocated space as free — it won't write there unless it runs out of room
- We reserve sufficient unallocated space during setup based on your storage needs
- Real PIN decrypts the hidden header and mounts the real volume instead

**Inspired by:** VeraCrypt hidden volumes, dm-crypt/LUKS detached headers, Tails persistent storage

### Data Separation

The Sentinel manages two parallel data paths in Go:

- Separate Postgres instances (different ports, different data dirs)
- Separate Redis instances
- Separate encryption keys derived from real/duress PIN
- The Mist (if enabled) maintains separate shard sets per volume

On unlock, the Sentinel checks which PIN was entered and configures all daemons to point at the corresponding data stores. The daemons themselves are identical — only the storage paths change.

### What We Use

| Component | Tool | Why |
|-----------|------|-----|
| Disk encryption | LUKS2 | Industry standard, audited, supports detached headers |
| Key derivation | Argon2id | Memory-hard, resists GPU attacks |
| Symmetric encryption | AES-256-GCM | Fast, authenticated, hardware-accelerated |
| Key storage | FIDO2 secure element | Hardware-bound, can't be extracted |
| Secure wipe | `blkdiscard` + `shred` | TRIM-aware for SSDs |

### Limitations

- Shadow volume must have enough "free" space to hide the real volume
- If you fill the shadow volume completely, you risk overwriting real data
- Setup wizard calculates safe boundaries and warns you
- Real volume size is fixed at setup (can't grow into shadow space)

---

## Mobile Technical Notes

- PIN entry handled in-app, not by OS biometric API
- Local SQLCipher database with two encryption keys
- Duress PIN decrypts decoy database
- Real PIN decrypts real database  
- Same app, same UI, different content

---

## Optional Features

- **Auto-purge:** Trigger full wipe after N failed unlock attempts (configurable, off by default)
- **Remote purge:** Signed message triggers destruction if you're separated from the box
- **Dead man's switch:** If not unlocked within N days, auto-purge (for worst-case scenarios)

---

## Summary

| Feature | Purpose | Implementation |
|---------|---------|----------------|
| Encryption | Protects data at rest | LUKS2 + AES-256-GCM |
| Hidden volumes | Protects from compelled access | Detached headers, unallocated space |
| Shadow system | Plausible deniability | Parallel data stores, same UI |
| The Purge | Nuclear option | Secure wipe + key destruction |

Privacy isn't just about keeping secrets. It's about maintaining control over what you reveal, and when, and to whom — even under duress.

```
THE ARCHITECTURE IS THE DEFENSE.
WE DON'T ROLL OUR OWN CRYPTO.
```
