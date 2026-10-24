# LocalGhost Connectivity & Device Trust

> The design note from before the build, kept for the reasoning. What is in place, and how it is
> set up and checked, is in `tools/README.md` (the setup, 8d for the edge's two shapes, 8e for
> `ghost-ctl doctor`); paths and names below (`/etc/localghost`, Let's Encrypt for the phone's
> port, a CRL, fail2ban) are not how the box ended up. The box CA signs the phone's certificate;
> a wrong PIN revokes the session; the firewall table is `tools/nft/localghost.nft`.

How the app reaches the NAS, and how the NAS knows it is really your device. No tunnel,
no VpnService, no NDK. Just HTTPS to a public endpoint, gated by a client certificate
the NAS issued to that specific device, with each request additionally signed.

Same mechanism on every platform: `app/android`, `app/iphone`, `app/web`. A phone, a
laptop, and a browser all authenticate identically — an HTTP client plus a key.

---

## The model in one line

The phone talks to `https://nas.yourdomain.com`. nginx rejects anyone without a client
certificate signed by **your** device CA, at the TLS handshake, before a request ever
reaches a daemon. Dynamic home IP is solved by DDNS pointing the hostname at the NAS;
the client never sees an IP.

```
phone ──HTTPS (mTLS)──> nas.yourdomain.com (DDNS) ──> nginx (verify client cert) ──> /v1/ daemons
```

Three independent layers of trust:
1. **mTLS** — transport gate. No valid device cert, no connection.
2. **Per-request signature** — each request signed by the device key (Ed25519).
3. **Device token** — the application-level identifier from the ingest contract.

Defence in depth: a break in one layer does not hand over the others.

---

## Why no tunnel

A WireGuard tunnel would mean `VpnService` + vendored `wireguard-go` + JNI + NDK build +
foreground service on Android, a Network Extension on iOS, and **nothing** on web. mTLS
is one HTTP client and one key on all three. The trade we accept: a public HTTPS endpoint
is visible to internet scanners, where a tunnel is silent. We mitigate with mTLS (drops
unauthenticated clients at the handshake), a minimal surface, and fail2ban. Network-level
invisibility via WireGuard stays on the table as optional future hardening, not a v1
blocker.

---

## Dynamic IP

The NAS has no static IP. A DDNS client on the NAS updates `nas.yourdomain.com` whenever
the WAN IP changes. The app always connects by hostname; DNS follows the NAS. No
rendezvous server, no endpoint discovery, no reconnect logic.

**Prerequisite — a routable address.** A public endpoint needs the home connection to
receive inbound on 443: a real public IPv4 and a port-forward. Confirm before relying on
this:

```bash
ip -4 addr show | grep inet            # NAS WAN interface address
curl -s https://api.ipify.org; echo    # what the internet sees
```

If they match → real public IP, port-forward 443, done. If they differ (CGNAT) → no
public endpoint reaches home without a relay, and the same is true of a direct tunnel.
That case needs a small relay box you control; out of scope for v1.

---

## Keys & certificates

### Device CA (lives on the NAS, offline-capable)
The NAS holds a **device CA** — a self-signed root whose only job is to sign per-device
client certs. Its public cert goes into nginx as `ssl_client_certificate`. Its private
key is the crown jewel: stored encrypted on the NAS, never on any device, ideally signed
operations happen behind a passphrase.

### Per-device identity
Every device (each phone, laptop, browser profile) gets:
- An **Ed25519 keypair** generated **on-device in secure hardware** — Android Keystore,
  iOS Secure Enclave/Keychain. The private key is **non-exportable**; even a rooted
  device cannot extract it.
- A **client certificate** for that keypair, signed by the device CA, carrying a stable
  `device_id` in the subject.
- The **device token** (ingest-contract bearer) bound to the same `device_id`.

The server pins/stores each device's **public key**; the private key never leaves the
device. "Private key validation" = the NAS verifies signatures and the cert chain
against keys it already trusts.

---

## Pairing (how a new device gets its cert)

The one moment that must be in-person / trusted, because it bootstraps all later trust.

1. **On the NAS**, the user generates a short-lived, one-time **pairing code** (e.g. a
   6-8 digit code or QR, valid ~5 min, single use).
2. **On the device**, the app generates its Ed25519 keypair in secure hardware and builds
   a CSR (certificate signing request) for the public key.
3. The app submits the CSR to a **pairing endpoint** that is open *only* while a pairing
   code is active, presenting the code:
   ```
   POST /v1/pair   { "code": "428913", "csr": "<pem>", "device_name": "Vlad S26" }
   ```
4. The NAS verifies the code, signs the CSR with the device CA, issues the device token,
   and returns the signed client cert + the **server CA cert to pin**.
5. The pairing code is now spent. From here the device authenticates with mTLS; the
   pairing endpoint is closed to it.

Pairing should happen on the local network (device + NAS on the same Wi-Fi) so the code
never traverses the internet. After pairing, the device works from anywhere.

---

## Certificate pinning (client side)

The app pins the NAS's server certificate / CA. A forged or MITM server cert — even one
from a publicly trusted CA — is rejected. Without pinning, mTLS authenticates the client
to the server but a CA compromise could let an attacker impersonate the server. Pin the
server CA returned during pairing.

---

## Revocation (lost or retired device)

Each device has its own cert, so trust is per-device:

- Revoke a device → add its cert serial to a revocation list nginx checks
  (`ssl_crl`), and invalidate its device token. That device is locked out on the next
  handshake; every other device is unaffected.
- No shared secret to rotate, no other device disrupted. This is cleaner than tunnel peer
  management.

```
# NAS: revoke one device
localghost device revoke <device_id>   # appends to CRL, kills token, reloads nginx
```

---

## nginx (the gate)

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name nas.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/nas.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/nas.yourdomain.com/privkey.pem;

    # require a client cert signed by the device CA
    ssl_client_certificate /etc/localghost/device-ca.pem;
    ssl_crl                /etc/localghost/revoked.crl;
    ssl_verify_client      on;
    ssl_verify_depth       1;

    # ingest + reflection API only — nothing else is exposed
    location /v1/ {
        proxy_pass         http://127.0.0.1:51017;
        proxy_set_header   X-Device-DN $ssl_client_s_dn;   # pass verified identity down
        proxy_buffering    off;     # SSE streaming for reflection/model responses
        proxy_read_timeout 600s;
    }

    # pairing endpoint: open only while a pairing code is active (app-enforced),
    # and ideally restricted to the local subnet
    location = /v1/pair {
        allow 192.168.0.0/16; deny all;
        proxy_pass http://127.0.0.1:51017;
    }
}
```

nginx hands the verified client DN to the daemons via `X-Device-DN`, so the application
layer also knows which device it is, on top of mTLS having already proven it.

---

## Per-request signing (layer 2)

Even inside mTLS, each request carries a signature so the daemon can verify integrity and
device identity independently of the transport:

```
Authorization: Bearer <device-token>
X-Device-Id:   <device_id>
X-Timestamp:   <unix-seconds>
X-Signature:   ed25519( device_id + "\n" + method + "\n" + path + "\n" + sha256(body) + "\n" + timestamp )
```

The NAS verifies the signature against the device's stored public key and rejects stale
timestamps (replay window, e.g. ±300s). This means a stolen token alone is useless without
the hardware-held private key.

---

## Hardening checklist

- [ ] Real public IP confirmed (not CGNAT); 443 port-forwarded.
- [ ] Device CA private key encrypted on NAS, never on a device.
- [ ] Per-device Ed25519 keys generated in Keystore / Secure Enclave, non-exportable.
- [ ] Server CA pinned in every client.
- [ ] `ssl_verify_client on` + CRL wired into nginx.
- [ ] Only `/v1/` exposed; pairing endpoint LAN-restricted and code-gated.
- [ ] Per-request signature + timestamp replay protection.
- [ ] fail2ban watching handshake/auth failures on 443.
- [ ] Pairing performed on local network only.

---

## What each platform implements

| | Android | iPhone | Web |
|---|---|---|---|
| Key storage | Android Keystore | Secure Enclave / Keychain | non-exportable WebCrypto key / platform |
| mTLS client cert | OkHttp + KeyStore | URLSession + Keychain identity | browser client-cert (limited) |
| Pairing | CSR + code on LAN | CSR + code on LAN | CSR + code on LAN |
| Request signing | Ed25519 via Keystore | Ed25519 via CryptoKit | WebCrypto Ed25519 |

Web is the weakest for hardware-backed keys and client-cert UX, another reason it stays
the dashboard (reflect/override) rather than the primary capture device.