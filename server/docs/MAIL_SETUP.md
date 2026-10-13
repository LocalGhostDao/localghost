# Setting up mail, step by step

For the person with a box and a domain. The relay's side is the web repository's
[`deploy/mail/README.md`](https://github.com/LocalGhostDao/web/tree/main/deploy/mail); the design is [MAIL.md](MAIL.md). The order matters: the box makes its keys first, the records
come from them, the relay's line comes last.

## 0. Before

- A box on a build with mail (0.0.7 or later), unlocked.
- The relay running at `mx.localghost.ai` (the web repository's `deploy/mail/README.md`), and
  its operator reachable to add one line and, for the shared domain, two DNS records. If that
  is you, do that page first.
- A decision about the address. The safe one is on LocalGhost's shared domain,
  `vlad@ghost.localghost.ai`: a sender learns nothing about you, and there is nothing of yours
  in DNS. Your own domain is only right if its zone holds nothing of the box: the name your
  phone connects to (`lgs.vladcealicu.com`) resolves to the box, so nothing under
  `vladcealicu.com` qualifies; a second domain with no record near the box would.

## 1. Tell the box its address and the relay

In the app, SETTINGS › MAIL: the address (`vlad@ghost.localghost.ai`), "on LocalGhost's shared
domain" on, the relay (`https://mx.localghost.ai`), a box id the relay will know you by
(`vlad`; lower case, digits and dashes), optionally the name mail leaves under, SAVE. Or on
the box:

```
sudo ./tools/ns.sh ./bin/ghost-cli ghost.noted mail-address address=vlad@ghost.localghost.ai shared=1
sudo ./tools/ns.sh ./bin/ghost-cli ghost.noted mail-relay url=https://mx.localghost.ai box=vlad
sudo ./tools/ns.sh ./bin/ghost-cli ghost.noted mail-from name="Vlad's ghost"
```

The box made its three keys the first time noted ran with the build (`<mount>/noted/mail/keys/`:
the X25519 key the relay seals to, the RSA and Ed25519 DKIM keys it signs with). Nothing here
leaves the box but public halves.

## 2. Give the relay your line

SETTINGS › MAIL › THE RELAY'S LINE (tap to copy), or `ghost-cli ghost.noted mail-enrol`:

```
box=vlad addresses=vlad@ghost.localghost.ai pub=<64 hex> token=<64 hex> dkim_rsa=<base64> dkim_ed=<base64>
```

The relay's operator appends it to `/etc/ghost-mail/boxes.conf` (the relay reloads the file
within a minute) and publishes your two DKIM keys under the shared domain, which
`ghost.maild -dns ghost.localghost.ai` prints for them:

```
vlad-rsa._domainkey.ghost.localghost.ai.  TXT  "v=DKIM1; k=rsa; p=…"
vlad-ed._domainkey.ghost.localghost.ai.   TXT  "v=DKIM1; k=ed25519; p=…"
```

The `token=` is a hash; the token itself stays on the box. The private keys stay on the box
too: LocalGhost publishes keys it cannot use.

## 3. Your own domain instead (only if its zone has nothing of the box)

Set the address to `agent@<that domain>`, the shared switch off, SAVE. SETTINGS › MAIL › DNS
RECORDS TO PASTE lists five records (each a tap to copy; or `ghost-cli ghost.noted mail-dns`):
the MX to `mx.localghost.ai`, the SPF `v=spf1 include:_spf.localghost.ai -all`, the two DKIM
keys under `vlad-rsa._domainkey` and `vlad-ed._domainkey`, and `_dmarc` with `p=reject`. The
relay's line then reads `domains=<that domain>`, and the operator adds it once
`dig +short MX <domain>` points at the relay. If the domain has an address record the box says
so in red and the answer is a different domain.

## 4. See it pull

Within two minutes the box asks the relay for its queue. SETTINGS › MAIL says "pulled just
now"; on the box, `ghost-cli ghost.noted mail` or `./tools/health.sh` (the mail line under
ghost.noted). "the relay refuses the token" means the line is not on the relay yet or the box
id differs; "relay unreachable" means the box cannot reach `mx.localghost.ai` on 443.

## 5. Send yourself something

From any mail account, write to `vlad@ghost.localghost.ai`. Within two minutes a notification
says "mail from you@…" with the verdicts, and MAIL shows it. Open it and tap the spam line to
see what the box checked; a message from Gmail should read SPF pass, DKIM pass by gmail.com,
DMARC aligned, reverse DNS ok.

## 6. Reply

REPLY, a line, SEND. SENT shows it "waiting at the relay", then "delivered" (or the reason it
was not). In your other mailbox, the message arrives from `vlad@ghost.localghost.ai`; "show
original" in Gmail shows `DKIM: PASS with domain ghost.localghost.ai`, `SPF: PASS`, `DMARC:
PASS`. If it lands in spam the first few times, that is the domain's youth as a sender and the
relay's; it passes with time and a few replies. If it bounces at once, read SENT's reason:
"550 … DMARC" means a DKIM record is missing or wrong; "no STARTTLS" means the receiver takes no TLS,
and the relay never sends in the clear.

## What to expect after

- Mail arrives while the box is locked too: the relay keeps it sealed for thirty days, and the
  first pull after unlock takes it all.
- A new sender raises one notification per message; a sixth message from one address in an
  hour is one line, the rest are kept quietly in MAIL.
- Attachments: a tap saves one to Downloads; executables, archives and macro-bearing office
  files are named and not kept, by design.
- The chat does not read the mail in this version, and nothing in a message is ever an
  instruction to the box.
- NEW TOKEN in SETTINGS › MAIL makes the relay's line wrong until the new one replaces it; use
  it if the old one may have leaked.
