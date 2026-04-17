# Security Policy

This document covers how to report a vulnerability in LocalGhost. For the security architecture (multi-PIN, decoy volumes, the purge), see [docs/SECURITY.md](./docs/SECURITY.md). For the threat model in longer form, see [The Honeypot Under Your Desk](https://www.localghost.ai/hard-truths/honeypot).

## Reporting a Vulnerability

Email `info@localghost.ai`, encrypted with our [PGP key](https://www.localghost.ai/.well-known/pgp-key.asc).

Please include:

- A description of the vulnerability and what it allows
- Steps to reproduce
- The LocalGhost version or commit affected
- Any proof-of-concept code or exploit details
- Your preferred credit line if you want to be credited

## What Happens Next

Acknowledgement within 72 hours. A real response (assessment, proposed fix, timeline) within 14 days. If the fix is going to take longer than 14 days, you'll get a reason why and a revised timeline.

## Disclosure Timeline

We ask for 90 days before public disclosure, or until a fix ships, whichever comes first. If a vulnerability is being actively exploited in the wild, we'll coordinate faster. If we can't fix in 90 days, we'll tell you why and agree an extension with you rather than silently let it slide.

## Scope

In scope:

- Any daemon in the LocalGhost fleet (`ghost.noted`, `ghost.framed`, `ghost.tallyd`, `ghost.synthd`, `ghost.voiced`, `ghost.shadowd`, `ghost.secd`, `ghost.mistd`, `ghost.watchd`)
- The hardware reference designs
- The installer and upgrade tooling
- Documentation that describes security-relevant behaviour incorrectly (that's a vulnerability in the instructions, which is still a vulnerability)

Out of scope:

- Vulnerabilities in dependencies we don't maintain (Postgres, Redis, LUKS2, llama.cpp, Ollama, etc.). Report those upstream. If one affects LocalGhost specifically, tell us so we can pin or patch.
- Social engineering attacks against the project or its contributors
- Physical attacks that require prior undetected access to the box for an extended period (cold boot attacks on a running box are in scope, cold boot attacks after an attacker has had it for a week are not)
- Denial of service that requires local root access

## What We Won't Do

**Pay a bounty.** We're Phase 0 and a small team. If the project grows and funds a bounty program later, we'll say so. We will credit you publicly with your permission, and we will take your report seriously.

**Sue you for responsible disclosure.** If you find a vulnerability, report it in good faith through the process above, and give us a reasonable window to fix it, we will not pursue legal action against you. This is a commitment, not a formality.

**Hide vulnerabilities after fixing them.** Post-fix, we publish an advisory covering what was found, what was affected, what shipped. The security of the system depends on the architecture being public.

## Not a Vulnerability

Some things get reported that aren't vulnerabilities. Flagging the common ones so we can handle them efficiently.

- **"Your duress system can be defeated by a nation-state adversary with prior knowledge of the architecture"**. Yes, documented in [docs/SECURITY.md](./docs/SECURITY.md) under "What v1 Is Built For". The architecture is built for border agents and wrench attacks, not state-level forensics.
- **"The project publishes its source code so attackers can read it"**. That's the design. [Kerckhoffs's principle](https://en.wikipedia.org/wiki/Kerckhoffs's_principle). Security through obscurity is not a property we're trying to have.
- **"Someone could steal the hardware"**. Yes, which is why the multi-PIN architecture and the purge exist. If you find a way to extract data from a seized box without the real PIN, that is a vulnerability, tell us.

## PGP Key

```
Available at https://www.localghost.ai/.well-known/pgp-key.asc
Fingerprint: [to be published with first release]
```

Verify before you send anything sensitive. If the fingerprint on the key doesn't match a trusted out-of-band source, don't encrypt to it.
