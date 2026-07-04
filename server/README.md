# ghost.secd

The keystone of a LocalGhost box. ghost.secd is the front door and the trust boundary: every request
from outside terminates here, gets authenticated, is bound to an unlocked account, and is proxied to
the right backing daemon. The other daemons (ghost.tallyd, ghost.voiced, ghost.shadowd, and the rest)
listen only on loopback and are never exposed. Auth, account selection, and the wipe logic all
live at this one chokepoint, so a daemon can only ever serve the account that is currently mounted.

```
internet ──TLS──> nginx ──> ghost.secd (authn + account routing) ──loopback──> ghost.<x>d
```

This document is the whole picture. The security model comes first because everything else follows
from it.

---

## Threat model , what this defends and what it does not

Be honest about the boundary, so it can be designed to instead of hoped for.

Defended:
- **Seize the box once, powered off.** Someone takes the box and images the disk. It is one full-disk
  LUKS container keyed by a TPM-sealed AMK; without a PIN-gated TPM unseal the disk is opaque and
  cannot be brute-forced offline (the AMK is 32 random bytes, the TPM rate-limits in hardware). This
  is the core threat and it is solid.
- **Stolen / seized phone, no box root.** The phone is a thin client; a wrong PIN makes the app appear
  identical to genuinely down (appears-down), the session token is revoked, and the background poller
  goes dark with the foreground. Brute-force lockout, Argon2id, constant-time checks rate-limit a
  guesser.
- **Coercion to wipe.** A wipe PIN crypto-erases everything (evicts the TPM AMK) and then presents
  EXACTLY like a wrong PIN. An onlooker cannot tell a wipe from a failed unlock.

Not defended, stated plainly:
- **Root on the box, live.** Root can read decrypted data while the account is mounted, scrape a PIN
  in memory during a real unlock, or patch the daemon. The TPM protects keys at rest and rate-limits
  guessing; it does not stop active root watching a live unlock. (The box and storage are TRUSTED in
  this model.)
- **Coercion that extracts the REAL PIN.** No software defends physical coercion: if they get your
  main PIN out of you, it opens. Appears-down hides the door and makes "it's down" true and
  unprovable; it does not stop a torturer. Border-agent-not-torturer.
- **The expert adversary, behaviourally.** Against someone who knows the LocalGhost design, an
  unreachable/down box reads as "owner declining to open", not "dead". Unprovable, not invisible.

Deniability stance: there are no on-disk decoys (equal-size decoy containers buy nothing against a
forensic imager once real data exceeds a third of the disk, and leak via block allocation). Deniability
lives on the phone (a thin client holding only recent data) and at the app layer (appears-down). "The
app is down" is unfalsifiable and mundane because the app genuinely is down sometimes.

---

## Access model , key first, PIN second

Two independent gates, in this order:

1. **Device cert (the key).** The box is its own CA and is always HTTPS. At setup it mints a client
   certificate for the phone; the phone receives it by scanning the QR (the box generates the key,
   the phone does not). nginx is configured with `ssl_verify_client on` against the box CA, so any
   connection without a box-issued cert is rejected at the TLS handshake , before it reaches
   ghost.secd, before any account or PIN. A scanner hitting the public IP gets a handshake failure
   and learns nothing. Reachability is not access.
2. **Account PIN.** Only once the device is trusted does ghost.secd care which account, proven by the
   PIN, with the wipe logic.

Both must fall for access: a stolen phone still needs a PIN; a known PIN is useless without an
enrolled device cert. This is why a public IP is safe , the door only opens for a key delivered by a
QR you physically showed, over your own SSH session.

Issuance is privileged: only the box `ghost`/root user can mint a device cert, so an attacker cannot
enroll their own phone remotely. The QR therefore carries a secret (the device private key), which
makes the QR itself a credential , shown once, over SSH, never stored. Setup clears it afterwards.

## Setup flow , `ghost.secd setup` (run as root)

Two phases, so nothing destructive happens until you have seen and confirmed the whole plan
(`setup/steps.go`, `setup/plan.go`):

1. **Dry run.** Walks every step WITHOUT touching anything: prints what it would do (including
   "partition /dev/X, this erases it"), checks preconditions (disk, nginx installed, DNS resolves,
   TPM usable), and stops you here if anything is wrong. Destructive steps are guarded so they can
   never run in this phase.
2. **Apply.** Only after a clean dry run and your confirmation. Runs in order, skips already-done
   steps (re-runnable), and stops at the first failure rather than half-provisioning.

What setup does, in order:

```
partition disk              (no-op: raw whole-disk LUKS, no partition table)
format container            one full-disk LUKS container, AMK sealed in TPM (DESTRUCTIVE)
ghost user                  unprivileged system user the daemons run as
box CA (self-signed)        the box becomes its own certificate authority
box server cert             the box's https cert, signed by the box CA, pinned by the phone
device cert                 the phone's client cert, delivered via the QR
nginx installed             checked, not installed for you
dns points at box           only with a domain; verified (reachability only, not cert issuance)
nginx config                mTLS: reject any client without a box-issued cert at the handshake
nginx reload
install systemd services    ghost.secd + every ghost.<x>d daemon, hardened units
enable + start services
tpm usable                  warns if not
clear setup artifacts       the QR carried a key: wipe history, temp files, the rendered QR
harden console              local console unusable as a bypass
```

All the operator does beforehand: point the chosen subdomain's A record at the box IP. Setup does
the rest. Validated: dry-run touches nothing, apply refuses a dirty dry run, destructive steps never
run in preview, apply stops at first failure, and the systemd units are hardened and correctly
ordered (daemons require ghost.secd; only ghost.secd gets TPM access).

## Certificates , the box is its own CA (no Let's Encrypt)

The box issues its own server cert and the phone's device cert from one self-signed CA. The phone
PINS the box cert (the fingerprint travels in the enrollment QR) and trusts that CA only. This is
stronger than Let's Encrypt for a personal box, not weaker: there is no public CA that could
mis-issue a cert for your domain, the phone trusts exactly one key , yours. It also removes the
Let's Encrypt operational fragility entirely: no port 80, no ACME challenge, no 90-day renewal that
breaks when your IP changes. The app pins the cert in its networking layer (a custom TrustManager /
CertificatePinner seeded from the QR), so self-signed is invisible to it , the "self-signed is
scary" reputation is a browser concern, and the app is not a browser visiting arbitrary sites. If
you ever want plain-browser access you can add Let's Encrypt for that path only; the app always pins.

## Domain and DNS , optional, with a privacy cost

`setup/domain.go` takes a domain (yours, or a name under localghost.ai), checks the A record resolves
to the box BEFORE standing anything up (`VerifyDNS`), and only then renders the nginx server block
(`NginxConfig`). nginx terminates public TLS and forwards to ghost.secd on loopback; it never sees
decrypted data or does auth, that all happens inside ghost.secd.

The honest trade is now small, because of the access model above. A public domain makes the box's
existence and address resolvable, and whoever runs the DNS zone can see the box's IP , but resolving
the box grants nothing, since nginx rejects every connection without a box-issued device cert at the
handshake. So a public name reveals "a box exists here and speaks mTLS", nothing more, and no account
or data. With no domain the box just lives on the LAN (the QR carries the LAN host); remote access is
opt-in later by adding a domain, and the cert gate is already there when you do. Hiding even that a
TLS service exists would need port-knocking or a VPN in front, a later option, not now.

---

## Gateway , the single front door

`gateway/` is the reverse proxy. The other daemons bind loopback only; ghost.secd authenticates every
request (mTLS plus the unlocked account) and routes by service name to the right daemon for the
MOUNTED account (`Router.Resolve`). A locked box refuses all routing (`ErrLocked`), so no daemon is
reachable until an account is open, and a daemon only ever receives requests for the account that is
mounted. The mounted account routes to its daemons.

---

## Auth , brute-force defence

`auth/` rate-limits PIN attempts with escalating delay and a hard lockout, stores only an Argon2id
hash, and compares constant-time. `Gate.CheckAllowed` / `RecordSuccess` / `RecordFailure` drive the
rate limiting where validity is decided by the profile registry. This fully defends a phone attacker
with no box root. Against box root it is bypassable; the TPM is the real defence there (`auth/tpm.go`).

---

## Profiles , the two-PIN model

`profile/setup.go` enforces a single real account plus a wipe PIN:

```
main PIN  opens the one account (slot 0, your real data)
wipe PIN  global crypto-erase, then presents EXACTLY like a wrong PIN
```

There are no decoys. `Accounts.Unlock` rate-limits, resolves the PIN constant-time against the
registry, then either opens slot 0 (main), or fires the global crypto-erase and returns Reject (wipe
PIN), or returns Reject (wrong PIN). A wipe is indistinguishable from a wrong PIN at the response
layer. The registry always holds a fixed number of entries, the two real PINs padded with random
filler, so the blob never reveals which PIN wipes or that a wipe PIN exists. Dissimilarity between the
two PINs is user guidance, not a code-enforced rule (an enforced rule would itself be a tell).

Deniability does NOT live on disk (see the threat-model docs): equal-size decoy containers buy nothing
against a forensic imager once real data exceeds a third of the disk, and they leak via block
allocation. It lives instead on the phone (a thin client holding only recent data) and at the app
layer (appears-down, below).

---

## Storage , one full-disk LUKS container, TPM-sealed key

`container/` is now just the `Mounter` seam. The account's container is the WHOLE raw disk
(e.g. `/dev/nvme1n1`), LUKS-formatted at setup , no partition table, no image file, no equal-size
juggling. The key is a random full-entropy account master key (AMK), sealed in the TPM bound to the
main PIN:

```
setup:   random AMK -> seal in TPM under main PIN -> luksFormat the raw disk with the AMK
unlock:  enter PIN -> TPM unseals AMK (hardware DA lockout on wrong PIN) -> luksOpen the disk
```

The AMK is never PIN-derived (so PIN entropy is not the key's entropy) and never leaves a TPM unseal,
so a powered-off stolen disk cannot be brute-forced offline , the TPM rate-limits in hardware. The AMK
exists in memory only long enough to seal + format at setup, then is zeroised. `--keyfile-size=32` is
used on every cryptsetup key feed so a random key containing a `0x0A` byte is never truncated.

---

## Wipe , crypto-erase by destroying the TPM-sealed key

`wipe/` makes destruction fast and irreversible without overwriting flash (wear levelling makes
overwrite unreliable, so it does not try). The disk is LUKS-keyed by the AMK, which is sealed in the
TPM. The wipe PIN evicts the TPM-sealed AMK (`EraseAll`): the AMK (32 random bytes) is gone, the LUKS
keyslot derived from it is useless, and 32 bytes cannot be brute-forced , the disk is permanently
undecryptable. Keys live in mlock'd buffers and are zeroised on wipe and unmount.

---

## Disk lifecycle , mount-on-unlock, persists till reboot

The disk is NOT in fstab: at boot there is no PIN, so no key, so nothing to mount (auto-mount would
hang). The encrypted data lives on the disk permanently as a locked LUKS blob. The first correct PIN
since boot does the real work (TPM unseal -> luksOpen -> mount -> start the in-volume Postgres/Redis).
The mount then PERSISTS until reboot. Re-lock happens only on reboot; a wrong PIN does not unmount (it
is an authorisation wall, not re-encryption). So after every reboot you must enter the PIN to bring
the data online , a powered-off or rebooted box is a locked box.

---

## Sessions , PIN every open, token for the rest

`session.go` holds the one runtime credential. The PIN is entered on every app open (~20-30x/day) and
is never stored or re-sent; it produces a token:

- correct PIN -> `Issue()` a fresh token (mounts + starts DBs if first unlock since boot)
- any wrong PIN -> `Revoke()` the current token (does NOT unmount)
- foreground AND the notification poller carry the SAME token (shared fate)

**Hybrid PIN verification:** the first unlock per boot does the real TPM unseal (hardware lockout
protects the at-rest key); subsequent opens verify against the registry hash (constant-time, the
volume is already mounted so the key is resident). Wrong PIN still revokes -> appears down.

---

## Appears-down , wrong PIN looks identical to the app being down

The deniability primitive for phone seizure. Someone past the mTLS device-cert gate (they hold your
enrolled phone) who enters a wrong PIN sees EXACTLY what the app returns when ghost.secd is genuinely
down. Because the app legitimately IS down sometimes (reboot-before-unlock, restarts), "down" is a
true, mundane, deniable state. Mechanism: wrong/expired token -> ghost.secd returns a bare 502; nginx
`proxy_intercept_errors on` + `error_page 502 503 504 = @down` renders ONE fixed generic response for
both "genuinely down" and "wrong token". Identical bytes by construction (induce the real condition,
do not imitate a response). `/v1/health` stays honest (how you check the box is up). Limits unchanged:
does not stop coercion that extracts the real PIN; a specialist who knows LocalGhost knows "down" might
mean "wrong PIN given". Unprovable, not invisible.

---

## Notifications + mute , the poller surface

`notifications.go` is what the app's background poller hits every ~15 min. Causes that converge on the
same appears-down 502 (an observer cannot tell them apart):

```
locked / DBs not up        -> down  (honest: nothing holding notifications is running)
wrong / expired token      -> down  (appears-down; shared fate with the foreground)
unlocked + GLOBALLY muted  -> down  (the global notification mute)
```

The mute is PER SCOPE: a GLOBAL mute (`*`) collapses the whole surface to "down" (a globally-muted box
looks exactly like a down one); a PER-SERVICE mute (`ghost.synthd`, `ghost.shadowd`, `ghost.cued`, ...,
one per notification-producing daemon) does NOT make the poll appear down , the app is not down, just
quieter , it filters that service out of the returned list. A service is muted if the global mute OR
its own per-service mute is active.

Durations: presets `1h` / `1d` / `1w`, `forever`, or a custom length (days + hours + minutes). Control
endpoint `POST /v1/notifications/mute` ({scope, preset|minutes/hours/days|clear|forever}); `GET`
returns active mutes for the settings screen. The scope is validated against the known daemon set
before any DB write. The mute lives in the in-volume Postgres (`notification_mute(scope, muted_until)`,
authoritative) cached in Redis (`notifications:mute:<scope>`, the poller's fast path), via
`hw/mutestore.go` (redis-cli + psql, no DB-driver dependency).

Notification data model (`hw/notifstore.go`): notifications are ALWAYS produced by the daemons
regardless of mute (mute affects PUSH, not storage). Each is stored in Postgres (`notifications`,
durable, `seen` flag, deletable forever) and LPUSH'd onto a Redis last-100 list
(`notifications:recent`). The in-app list reads the full Postgres history; the poller reads the push
window. Push is Option A , a per-device high-water cursor (`notifications:cursor:<dev>`): read last-100
-> take id > cursor -> advance cursor past ALL of them (muted included) -> THEN drop muted from the
payload. Advancing before removing muted is load-bearing: a muted notification is skipped from push and
never pushed later, but stays in the store with its seen/delete state for the in-app list , muting
suppresses push, it does not delay-and-replay. Endpoints: `/v1/notifications/list`, `.../seen` ({id}),
`.../delete` ({id} forever). On any down response the app wipes its local notification cache.

---

## Per-account databases , initialised in the volume at setup

`hw/datastore.go` runs a dedicated Postgres + Redis per account, INSIDE the encrypted container (data
encrypted at rest, vanishes on crypto-erase, runs only while mounted). On first start it `initdb`s the
cluster, generates a RANDOM db password (like the AMK: random, never PIN-derived, stored only inside
the volume at `db-credentials.env`), and lays down the app config schema (`settings`,
`notification_mute`). Unlock's StartDB/StartCache stages then just start them per boot. Ports are
loopback-only, derived per slot.

---

## The hardware seam (the arbiter)

One backend turns this from structurally-verified logic into a running system: the TPM + dm-crypt +
per-account DB integration, built with `-tags tpm`. On this box (bare-metal Debian 13, Intel PTT)
there is no VM, so no vTPM/hypervisor gap. The seam covers:
- `hw/tpm.go TPMSealedKey` , seal/unseal/evict the AMK, PIN-gated, hardware lockout.
- `hw/dmcrypt.go DMCryptMounter` , luksOpen the raw disk with the unsealed AMK, mount, unmount.
- `hw/datastore.go DataStore` , per-account Postgres/Redis inside the mounted volume.
- `setup/debian/system.go` , the raw-disk luksFormat + AMK seal + registry write at provisioning.

NONE of this has run yet , it is structurally verified only. The box compiler (`make box TAGS=tpm`)
and a real `ghost-setup --apply` are the arbiters.

---

## Layout

Each package has its own `README.md` with its purpose, key types, and gotchas , this is the index.

```
secd/         the ghost.secd front door: HTTP surface, sessions, appears-down, notifications, mute
auth/         brute-force gate, Argon2id credential, TPM seam
profile/      registry (constant-time, count-hidden), two-PIN setup policy, Accounts.Unlock
container/    the Mounter seam (raw-disk LUKS, single container)
integration/  per-account connectors, paused-by-default hygiene
wipe/         crypto-erase (evict the TPM-sealed AMK), mlock'd secrets, hardware-erase seam
gateway/      reverse proxy: loopback daemons behind one authenticated front door
hw/           TPM seal/unseal, dm-crypt mount of the raw disk, per-account Postgres/Redis,
              mute + notification stores (real hardware / in-volume, -tags tpm for the TPM parts)
models/       phone-runnable model catalogue + byte server (unencrypted, shared)
admin/        PIN rotation (resetup-*), fail-closed local-network gate, sshd binding check
setup/        ordered idempotent setup plan; nginx/systemd/DNS/cleanup steps, appears-down config
setup/debian/ the real Debian box backend: raw-disk LUKS + AMK seal + registry write (-tags tpm)
pair/         self-contained QR enrollment (no server)
cmd/ghost.secd, cmd/ghost-setup (the wizard), cmd/ghost-ctl (CLI client)
```

## Build and test

```
go test ./...
```

No external services are required for the tests. The security-critical logic (registry resolution,
per-account crypto-erase, the unlock decision tree, DNS verification, routing) is unit-tested; the
hardware and dm-crypt pieces are the seams above, tested on the box.

## Dependencies

One, deliberate: `golang.org/x/crypto` for Argon2id (the one password KDF that is not in the standard
library and must not be hand-rolled). Everything else , AES-GCM, HKDF (stdlib `crypto/hkdf` on go
1.24+), X.509/mTLS, SHA-256, the QR encoder (own implementation in `pair/qrencode.go`) , is the
standard library. The real box build additionally links `github.com/google/go-tpm` behind the `tpm`
build tag; the default build stays on the single dependency above.


## Unlock progress , streamed, and identical across accounts

A cold unlock is not instant: the container mounts (TPM unseal + dm-crypt), then the account's
Postgres and Redis start. `profile/unlock_stream.go` streams the stages (checking, unsealing,
mounting, starting database, starting cache, starting services, ready) so the app shows a real
loading state instead of a freeze.

The stream is IDENTICAL for every unlock. A wipe-PIN entry emits the same stages, in the same order,
with the same labels, as a real one, so the progress reveals nothing about which account is opening
(validated: two cold unlocks are byte-identical). The only legitimate variation is warmth: an
already-mounted hot account reports the heavy stages as Skipped (skip, skip, skip, fast), a cold one
runs them. Warmth tracks how recently you used the box, not which account is real, so it is not a
tell , and it is why a hot unlock is fast while a cold one honestly takes its time.

This also gives the cold start an honest place in the product: it is just how the box starts a cold
account, neither hidden nor dramatised. The deniability under coercion lives in the wipe PIN (erases
everything, presents like a wrong PIN) plus whatever you choose to say , not in the timing. The system
does not claim anything; it loads honestly and the same way every time.


## PIN rotation , `ghost.secd resetup-*` (box-only, local network)

There is no PIN change without data loss, by design. A PIN is bound to its volume's key, so rotating
a PIN means destroying the old key (the data is crypto-erased) and creating a fresh volume keyed by
the new PIN. Reset equals wipe. This is deliberate: it means there is no rotate-in-place admin
surface for an attacker to abuse, and no way for a coerced phone to rotate anything.

Rotation is reachable ONLY from the box, over a local-network session , never from the app, never
remotely:

```
ghost.secd resetup-main      erase + re-create slot 0 with a new PIN
```

Each command names its slot explicitly and touches only that slot , no relative roles, no cross-slot
indirection. The gate (`admin/local.go`, validated) FAILS CLOSED: it reads the session's real peer
address and refuses unless it is provably loopback/RFC1918/link-local; anything public, ambiguous, or
undeterminable is rejected. Defence in depth: setup also verifies sshd is not listening on a public
interface (`admin/sshd.go`) and warns if it is.

The safety ordering is the important part (`admin/resetup.go`, validated): the command shows
"you are about to ERASE the <slot> partition (<size>), this is permanent", then takes the new PIN
TWICE, and destroys the old key ONLY after the new PIN is entered and confirmed. A typo or mismatch
aborts and leaves the slot untouched, so a slip never costs the volume. The PIN is zeroised from
memory on every path. CommitReset wipes and re-creates atomically, so there is no window with neither
key.

Honest limit: "local network" means a private/loopback source address, not "physically on my LAN". A
VPN or port-forward landing a peer in a private range would pass. For this threat model that is
acceptable (anyone already on the LAN/VPN has broad access); tighten to loopback-only (SSH to
localhost / console) if you want to close even that.


## Models , unencrypted, shared across accounts

`models/` serves phone-runnable models. They live in the UNENCRYPTED system area (e.g.
`/var/lib/ghost/models`), beside where the apps run, NOT inside any account's container. Models are
public artifacts identical for every account , the same as the daemon binaries , so encrypting them
per account would buy nothing and cost copies of multi-gigabyte weights. Reading or serving a
model therefore needs no mounted account and no PIN, and carries no personal data, so it does not
weaken per-account isolation.

ghost.secd reads the catalogue (`catalog.json`: id, name, detail, sizeBytes, sha256) when the phone
asks what is available, sorted smallest-first since those run best on a phone, and streams a model's
bytes for the phone to download and run locally. The byte server validates the id against the
catalogue and refuses any path that escapes the models dir (traversal guard), and the catalogue
carries a SHA-256 the phone verifies after download (and `Verify` lets the box confirm an installed
model is intact). Validated.


## Unlock: simulation by default, real hardware with -tags tpm

The unlock flow (PIN to a mounted, running account) goes through the `UnlockBackend` seam
(`server/backend.go`). There are two implementations selected by build tag, so ghost.secd compiles
and the app's unlock flow is testable on any machine, while the real hardware path is a one-flag
switch on the box:

  go build ./...            default: simulation backend (server/backend_sim.go). Opens the main slot
                            for any non-empty PIN and simulates the cold-unlock cost. NOT a security
                            boundary , for development and the app's loading UI off-box only.

  go build -tags tpm ./...  real backend (server/backend_tpm.go). Wires:
                              profile.Accounts   PIN resolution (main / wipe / reject)
                              hw.TPMSealedKey     per-slot seal/unseal against /dev/tpmrm0 (Intel PTT)
                              hw.DMCryptMounter   LUKS map + filesystem mount of the slot container
                              hw.DataStore        per-account Postgres + Redis inside the volume
                            Needs go-tpm in the module (go get github.com/google/go-tpm), a TPM,
                            root, cryptsetup, postgres and redis. Exercise on the box.

The shared stage logic (`runUnlock`) lives in one place so the sequence and timing-uniformity (a
a wipe-PIN entry looks identical to a wrong PIN) are the same in both builds. The key is unsealed once
and zeroised right after the mount consumes it.

The account registry is persisted by `profile.Registry.Save` / `LoadRegistry` (`profile/persist.go`)
in the unencrypted state area. The on-disk form holds ALL RegistrySize entries , real and random
filler , in a fixed-size layout, so the file never reveals how many real PINs exist (the count-hiding
property that gives the deniability its teeth survives a restart). It stores only salted PIN hashes,
never PINs or keys. Validated: round-trip preserves every PIN's resolution incl the wipe
target, and the file size is identical for one real PIN or many.

Honest limit, restated: the TPM and dm-crypt paths are built against the documented go-tpm and
cryptsetup interfaces and are NOT validated in CI (no TPM, no root, no encrypted volumes in the build
env). They must be exercised on real box hardware.
