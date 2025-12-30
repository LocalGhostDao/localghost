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

---

## The Shadow System

This isn't a wipe. It's a second reality.

The shadow system is a complete, functional LocalGhost installation with its own:
- Folder structures that mirror a real setup
- Plausible journal entries (generated, bland, boring)
- Photos, documents, notes — all fake but believable
- Light Auditor data (a few bank transactions, nothing interesting)
- No Observer recordings (you "never enabled that feature")
- Weaver correlations that lead nowhere useful

The decoy is generated during setup and periodically refreshed. It looks like a real person's data — just not yours. Boring enough to be credible. Empty enough to disappoint.

**There is no evidence the real system exists.** The shadow system uses all available disk space from its perspective. Forensic analysis shows a normal encrypted volume with normal data. The real volume is hidden within the "free space" — indistinguishable from random noise.

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

## Mobile: Biometric Duress

On mobile, the same principle applies with fingerprints:

| Finger | What Happens |
|--------|--------------|
| **Right thumb** | Real system. Full access. |
| **Left thumb** | Shadow system. Plausible decoy. |

Different fingers, same phone, completely different realities. Under coercion, you unlock with your left hand. Natural, compliant, cooperative. They see a boring person's data. Your real life stays hidden.

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

## Technical Notes

- Single FIDO2 key with two PIN slots (real + duress)
- Hidden volume design inspired by VeraCrypt — real data lives in "free space" of shadow volume
- Shadow system generated during setup, refreshed periodically with plausible decoy data
- Decoy data uses local LLM to generate believable but fictional journals, notes, metadata
- Real volume encrypted with different key, only accessible with real PIN
- Forensic analysis shows single encrypted volume with consistent data — no evidence of hidden volume
- The Mist shards are encrypted per-volume — duress PIN only retrieves shadow shards
- Purge wipe uses `shred` + random overwrites (TRIM-aware for SSDs)
- Optional: auto-purge after N failed unlock attempts (configurable, off by default)
- Optional: remote purge trigger via signed message (if you're separated from the box)
- Mobile: iOS/Android app registers two biometric profiles during setup

---

## Summary

| Feature | Purpose |
|---------|---------|
| Encryption | Protects data at rest |
| Hidden volumes | Protects you from compelled access |
| Shadow system | Plausible deniability under inspection |
| The Purge | Nuclear option when data must be destroyed |

Privacy isn't just about keeping secrets. It's about maintaining control over what you reveal, and when, and to whom — even under duress.

```
THE ARCHITECTURE IS THE DEFENSE.
```
