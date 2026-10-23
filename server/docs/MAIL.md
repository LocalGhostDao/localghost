# Mail for the ghost

The box has an address of its own. It is on LocalGhost's shared mail domain
(`vlad@ghost.localghost.ai`), or on a domain of the person's own if they have one whose zone
holds nothing of the box; either way the mail is carried by a relay LocalGhost runs at
`mx.localghost.ai` that keeps nothing it can read and parses nothing it sees. Built 12 October
2026, standard library only (`net`, `crypto/tls`, `net/textproto`, `net/mail`, `mime`,
`net/smtp`) plus the sealing the backups already use (`crypto/ecdh` X25519, HKDF, with the
standard library's AES-256-GCM in place of x/crypto's ChaCha20 so the relay needs no
dependency, in `internal/mailseal`). This page is the specification and the map of the box's
code. The relay is in the web repository,
[`deploy/mail/`](https://github.com/LocalGhostDao/web/tree/main/deploy/mail), its own Go module
with no dependency beyond the standard library; its `README.md` is the operator's page and its
`PROTOCOL.md` the contract both sides hold to.

## Why the address is not on the box's own name

A sender has the domain after the `@`, and every domain is one lookup from everything in its
zone. The name the phone connects to the box by (`lgs.example.com`) resolves to the box's
own address; an address under that name, or anywhere under `example.com`, hands a sender a
zone with the box in it (the name is guessable, in certificate transparency logs, in passive
DNS). So the address lives on a domain that has nothing of the person's in it at all: the
relay's shared domain, `ghost.localghost.ai`, where an address is one local part among many and
the zone is LocalGhost's. A person who owns a second domain with nothing of the box in it may
use that instead (`agent@` it); the box warns when a chosen domain has an address record, and
the rule is theirs to keep for the rest of the zone. In both cases the box's IP appears nowhere:
not in DNS, not in a `Received` header (the relay's name is in those), not in anything the box
sends, because the box only ever speaks outbound, over HTTPS, to the relay. The `Date` and
`Message-ID` of a sent message are made on the box without its clock's zone or its host name.

## The shape

```
sender's MTA ──25/TLS──▶ ghost.maild (mx.localghost.ai): seal(bytes), seal(note) ──queue──▶ box pulls (HTTPS, unlocked only)
                                                                                                   │
                                                      ghost.noted: unseal, judge (SPF, DKIM, DMARC, rDNS, headers, spam),
                                                      parse (text, attachments), store, one notification
                                                                                                   │
                                                      secd /v1/mail/* ◀──▶ the app's MAIL (list, read, reply, forward, write)
                                                                                                   │
recipient's MX ◀──25/TLS── ghost.maild delivers, retries a day, bounces back ◀──HTTPS── noted: build, sign (DKIM), seal, post
```

The box never listens on the internet. Mail needs a machine on port 25 at a fixed address, so the
receiver is the relay, one small daemon on the machine LocalGhost's site runs on, kept in the
web repository because that is whose machine it is. It takes a message in over
TLS, seals it to the box's key as the bytes arrive, writes a sealed note of what only it could
know about the connection, and keeps the pair in a queue until the box has fetched them. The
box pulls every two minutes while it is open (noted runs inside the vault, like every daemon
but secd), judges and keeps each message, and deletes the relay's copy. Sending is the same
road back: the box builds and signs the message, seals it to the relay's key, posts it; the
relay delivers it to the recipient's server with STARTTLS, retries a day, and writes a bounce
into the box's queue when it cannot.

## What a sender learns, and what the relay learns

A sender learns the domain after the `@` and, with one lookup, the relay's IP; on the shared
domain that is LocalGhost's zone and LocalGhost's machine, and nothing of the person's.

The relay cannot know nothing, and this page does not pretend it can. SMTP delivers plaintext
over a TLS session that ends at the relay, so for the seconds a connection lasts the relay has
the sender's address, the recipient's, and the bytes, in memory. The design makes that the whole
of it. The relay **keeps** nothing readable (every byte streams into the sealer against the
box's X25519 key, AES-256-GCM, the plaintext never touches the disk, the log counts and never names), and it
**parses** nothing (no header parser, no MIME, no DNS on a stranger's content; the one lookup
it does is the reverse name of the connecting IP). The verdicts are computed on the box from the
sealed message and the sealed note. A relay that is rooted exposes mail arriving after that
moment and nothing before it; only end to end closes that last gap, and it is a later layer.

## DNS

**On the shared domain** the records are LocalGhost's. Once for the domain:

```
ghost.localghost.ai.          MX   10 mx.localghost.ai.
ghost.localghost.ai.          TXT  "v=spf1 include:_spf.localghost.ai -all"
_dmarc.ghost.localghost.ai.   TXT  "v=DMARC1; p=reject; adkim=s; aspf=r"
```

and per box, its two DKIM public keys under its own selectors, which the box signs with:

```
vlad-rsa._domainkey.ghost.localghost.ai.  TXT  "v=DKIM1; k=rsa; p=<the box's RSA-2048 key>"
vlad-ed._domainkey.ghost.localghost.ai.   TXT  "v=DKIM1; k=ed25519; p=<the box's Ed25519 key>"
```

The keys travel in the box's enrol line (`dkim_rsa=`, `dkim_ed=`), and the relay prints the
zone lines for every box with `ghost-maild -dns ghost.localghost.ai`. The private halves never
leave the box's volume, so LocalGhost publishes keys it cannot use, and `p=reject` with strict
DKIM alignment means only the box can send as its own address.

**On a domain of the person's own** the same five records, at their DNS provider, shown ready
to paste in SETTINGS › MAIL (and by `ghost-cli ghost.noted mail-dns`): the MX to the relay, the
SPF including `_spf.localghost.ai` (one record LocalGhost keeps current, so a relay IP change
never touches a box's DNS), the two DKIM keys under `<box>-rsa` and `<box>-ed`, and `_dmarc`
with `p=reject`. Two keys because the largest receivers still verify only RSA; the box signs
with both.

## The relay, `deploy/mail/cmd/ghost.maild` in the web repository

Runs as a system user of its own, `ghost-mail` (no shell, no home), under a hardened system
unit that lets it write one path, `/var/lib/ghost-mail` (the queue, its own key, the TLS
copies), and read the deploy user's `~/ghost-mail/{bin,etc}` (the binary, the configuration,
`boxes.conf`); it binds 25 by capability. Root is needed once on the machine
(`scripts/root-once.sh`: the user, the unit, a certbot deploy hook that copies the mx
certificate's PEMs into `/var/lib/ghost-mail/tls`, and the deploy user's leave to restart the
unit without a password); an update is the deploy user's `install.sh`, whose one sudo is that
restart. Until 18 October 2026 it ran as the deploy user itself, a user unit on a lowered
unprivileged-port floor; a parser that takes bytes from any host on the internet should not
own the machine's websites. `:25` for SMTP, `127.0.0.1:8025` for
the box API behind the host's nginx. Its own A record, a PTR at the host's provider pointing
back, a Let's Encrypt certificate reloaded within ten minutes of a renewal, port 25 open both
ways. `deploy/mail/` over there has the unit, the config examples, the hook, the nginx snippet,
the MTA-STS policy, `scripts/root-once.sh` and `scripts/install.sh`. Its `internal/seal` and
`internal/wire` are this repository's `internal/mailseal` and `internal/mailwire`, the same
files: the seal format and the note are the contract, and neither side changes them alone.

**Boxes.** `~/ghost-mail/etc/boxes.conf`, one line per box, written by hand (no `/enrol` in the
first version; see "Who may use the relay"):

```
box=vlad addresses=vlad@ghost.localghost.ai pub=<X25519, 64 hex> token=<sha256 of the bearer> dkim_rsa=<base64> dkim_ed=<base64>
box=ana  domains=ghost.ana.example      pub=… token=… dkim_rsa=… dkim_ed=…
```

`addresses=` are single addresses on a shared domain; `domains=` are domains the box owns
whole. A `RCPT TO` is matched by exact address first, then by domain, so a stranger's local
part on the shared domain is `550`. The line comes from the box itself (SETTINGS › MAIL shows
it; `ghost-cli ghost.noted mail-enrol` prints it): the box makes the token and keeps it; the
relay holds only its hash, and the DKIM public keys only so `-dns` can print them. The file is
reloaded when it changes.

**Receiving** (`smtp.go`). `EHLO`/`HELO`, `STARTTLS`, `MAIL FROM`, `RCPT TO`, `DATA`, `RSET`,
`NOOP`, `QUIT`, `VRFY` (252), `HELP`; `SIZE` 25 MB, `8BITMIME`, `PIPELINING`,
`ENHANCEDSTATUSCODES`. No `AUTH`, no `SMTPUTF8`, no relaying: a `RCPT TO` whose domain is in no
box's line is `550 5.1.1`. TLS is required: `MAIL` before `STARTTLS` is `530 5.7.0 Must issue a
STARTTLS command first`, TLS 1.2 and up. The `DATA` bytes, dot-unstuffed and CRLF, stream
straight into one sealer per recipient box under a `Received:` line naming the relay, the
HELO, the reverse name and the IP. Beside each message goes the **note**, sealed the same way:
the connecting IP and port, the HELO, the envelope, the TLS version and cipher, the time, the
size, and the forward-confirmed reverse DNS verdict (ok, none, mismatch, error). Both files
appear under their final names only once both are complete. Limits: twenty connections, five
per address, four messages a minute per address (421), ten recipients, a line over 2000 bytes
refused, a minute's silence or ten minutes of `DATA` ends the session, five bad commands end
it; a box's queue over 200 MB is `452 4.2.2 mailbox full` (senders retry); greylisting
(`greylist=on`, a 451 for a never-seen sender, taken on the retry two minutes later) off until
needed. Nothing is refused on content, because the relay never looks at content. The queue
holds a sealed file thirty days.

**The API** (`api.go`), bearer token, the box id in the path and the token must match:
`GET /box/<id>/in/` lists `{name, size, note}`; `GET /box/<id>/in/<name>` returns a sealed
file (the note is `<name minus .seal>.note.seal`); `DELETE /box/<id>/in/<name>` removes both;
`GET /box/<id>/key` is the relay's X25519 public key and host name; `POST /box/<id>/out` takes
`{from, to[], sealed}`; `GET /box/<id>/out/<name>` is that message's state. Names are
`<unix nanoseconds>-<16 hex>.seal` and nothing else reaches a path.

**Sending** (`deliver.go`). The relay unseals the posted message in memory with its own key,
checks what is structurally the box's (the envelope sender and the `From` header one of the
box's addresses or on a domain it owns; a `DKIM-Signature` with `d=` the `From` address's
domain, since the box signs and the relay never does), takes from the box's allowance (a hundred recipients a day, ten an hour, per box; the
daemon's or the line's `sendday=`), seals the message to itself and queues it with a JSON job.
Every thirty seconds the queued jobs whose time has come are tried, domain by domain: the MX
hosts in preference order (the A record when there is no MX; a null MX means the domain takes
no mail), one SMTP session each with `EHLO`, `STARTTLS` verified against the host's name (a
failed verification is retried once unverified, so the hop is always encrypted and
authenticated when it can be; a host that offers no STARTTLS is skipped, nothing of a box's
leaves in the clear), `MAIL`, `RCPT`, `DATA`. A 5xx is final for that recipient; a 4xx or a
connection error retries after 1, 5, 15, 60, 120, 240 minutes and then every four hours, for a
day from acceptance. When nothing is pending the job is `sent`, or `bounced` with a report
written into the box's inbound queue as a message from `mailer-daemon@<relay>` (sealed, with a
note whose reverse verdict is `relay`), so the box learns of it the way it learns of any mail.

**The log** says counts, sizes, box ids and refusals, never an address.

## The box, `cmd/ghost.noted` (`mail.go`, `mailparse.go`)

At first run noted makes three keys under `<mount>/noted/mail/keys/`: `mail.key` (X25519, what
the relay seals to), `dkim_rsa.key` (RSA-2048, PEM) and `dkim_ed.key` (Ed25519). It writes
`dns.json` (the records, the seal public key, the relay's line and the warning when the
domain has an address record) for secd and the app, and `status.json`
after every pass. Settings live in the `settings` table: `mail_address`, `mail_shared`, `mail_relay_url`,
`mail_relay_box`, `mail_relay_token`, `mail_relay_pub` (pinned on first use), `mail_relay_host`,
`mail_from_name`, `mail_dns_checks`.

**The pull**, every two minutes while the box is open (`ghost-cli ghost.noted mail-pull` for
now): list the queue; a name already stored is deleted on the relay and skipped (a pull cut
short after the insert stores a message once); otherwise fetch the note and the message,
unseal both in memory, write the sealed message as fetched to `<mount>/noted/mail/in/`, then:

1. **Judge** (`internal/mailauth`, `Judge`): SPF for the note's IP against the `MAIL FROM`
   domain (or the HELO for a bounce): `ip4`, `ip6`, `a`, `mx`, `include`, `exists`, `ptr`,
   `redirect=`, the macros, ten lookups at most; every DKIM signature (RSA-SHA256 and
   Ed25519-SHA256, relaxed and simple, `l=`, `x=`, the key at `<s>._domainkey.<d>`, testing
   keys neutral, revoked keys fail); DMARC alignment with the `From` domain's policy (the
   organisational domain by a short public-suffix table, relaxed or strict as the record
   says); the note's reverse verdict and whether it matches the HELO; the **header screen**
   (more than one `From`, `Date`, `Subject`; a bare CR or LF inside a field; a line over 998
   bytes; a `From` with two addresses or none; control characters or a second address in the
   display name; a display name that is an address on another domain, or that pretends to be
   this box; a `Reply-To` on another domain; no `Message-ID` or `Date`; a subject all in
   capitals; a body that is HTML only); and a **spam score** from those and from whether the
   sender has written before (low, medium, high, with the reasons). DNS goes through the
   system resolver with a five-second timeout; `mail_dns_checks=off` turns it off and the
   verdicts read "not checked". A note whose reverse verdict is `relay` is a delivery report.
2. **Parse** (`mailparse.go`): `net/mail` for the headers (encoded words decoded, Latin-1 and
   Windows-1252 handled by hand, UTF-8 otherwise), the MIME tree walked eight levels deep: the
   first `text/plain` part is the body, else the `text/html` reduced to text (scripts and
   styles dropped, block ends as line breaks, entities decoded, nothing fetched); every other
   part is an attachment. What runs or hides things that run is **named and not kept**:
   executables by extension and by magic (`MZ`, ELF, Mach-O), archives (`PK` unless it is an
   office or EPUB file), office files with macros (`.docm` and friends, or an OLE file with a
   VBA stream); so is anything over 20 MB or past 25 MB in all. Names are reduced to a safe
   character set and a base name.
3. **Store**: one `mail_messages` row (direction, state, Message-ID, In-Reply-To, the thread
   key, From, To, Cc, Reply-To, subject, the dates, the text, its kind, the size, the verdicts
   as JSON, the spam level, the sealed file's path, the relay's name) and one
   `mail_attachments` row per part, the kept bytes under `<mount>/noted/mail/att/<id>-<name>`.
   The thread key is the first `References`, else `In-Reply-To`, else the message's own id.
4. **Notify**: one notification per message (`ghost.noted`, kind `mail`, link `mail:<id>`):
   "mail from paul@example.org", body the subject and the verdict line ("SPF pass · DKIM pass by
   example.org · DMARC aligned · reverse ok · spam low"). A sixth message from one address in
   an hour is one line saying so; the rest are kept quietly.
5. Delete the relay's copy.

**The outbox**: secd drops a JSON request in `<mount>/noted/outbox/` for a tap on SEND; noted
builds the message (`From` with the name set, `To`, `Cc`, `Subject` encoded, `Date`,
`Message-ID` `<random@domain>`, `In-Reply-To` and `References` for a reply, the text as
quoted-printable UTF-8, forwarded attachments as a `multipart/mixed` with base64 parts, no line
over 998 bytes), signs it with both DKIM keys, selectors `<box>-rsa` and `<box>-ed` (relaxed/relaxed over From, To, Cc, Subject, Date,
Message-ID, In-Reply-To, References, Reply-To, MIME-Version, Content-Type,
Content-Transfer-Encoding and the body hash), stores the row (`out`, `queued`), seals to the
relay's pinned key and posts. Each pass asks the relay about every queued message and records
`sent` or `bounced` (with the receivers' reasons). A reply quotes the original under the text
("On 12 October 2026 at 10:00, Paul <paul@example.org> wrote:" and `> ` lines) when asked; a
forward puts a header block and the original text under it and re-attaches what was kept.

**The ctl**: `ghost-cli ghost.noted mail` (status, settings, the last twenty rows),
`mail-pull`, `mail-address address= shared=1|own=1`, `mail-relay url= box= token=`,
`mail-enrol` (the line for the relay's `boxes.conf`; makes the token when there is none),
`mail-dns` (the records),
`mail-from name=`, `mail-checks on|off`. `tools/health.sh` prints a mail line under
`ghost.noted`.

## secd, `/v1/mail/*`

`GET /v1/mail?folder=inbox|sent|archive|all&q=&limit=&before=` lists newest first with the
counts; `GET /v1/mail/message?id=` is one message whole with its thread, and marks it seen;
`POST /v1/mail/state {id, state}` (seen, new, archived); `POST /v1/mail/delete {id}` removes
the row, the attachments and the sealed original; `GET /v1/mail/attachment?id=` serves the
bytes as a download with `nosniff` (HTML, SVG and scripts as `application/octet-stream`);
`POST /v1/mail/send {to[], cc[], subject, body, replyTo, forwardOf, quote}` writes the outbox
file and chowns it to the run user; `GET /v1/mail/status` is the settings, noted's status
line, the DNS records, the relay's line and the warning (the token is made here if the box has
none, and never travels to the phone); `POST /v1/mail/settings {address, shared, relayUrl,
relayBox, fromName, dnsChecks, newToken}`. The schema (`mail_messages`, `mail_attachments`) is in the declarative
registry and converges at unlock.

## The app, MAIL

In the drawer under YOUR ARCHIVE. The list has INBOX, SENT and ARCHIVE, a search over sender,
subject and text, the counts, and a row per message (who, when, the subject, the preview or
what happened to a sent one, `new`, the files, `spam?`); older pages load on a tap. A message
shows who and when (a `Reply-To` elsewhere in amber), one line of the spam verdict with the
checks under a tap (each verdict in plain words), the text as text (an HTML message says it
was reduced), the attachments (a tap saves a kept one to Downloads; the app opens nothing), and
REPLY, ALL (when there were others), FORWARD, ARCHIVE, UNREAD, DELETE (armed by a first tap),
then the thread. The composer is to, cc, subject, text, "quote the message under your text"
for a reply, and SEND, which hands the message to the box; SENT shows it waiting at the relay,
delivered, or not delivered with the reason. A notification's tap opens the message. SETTINGS
› MAIL holds the address, whether it is on the shared domain or the person's own, the relay,
the box id, the name, the DNS lookups switch, the records (tap to copy; on the shared domain
shown for checking, LocalGhost publishes them), the relay's line (tap to copy), NEW TOKEN, and
the warning when an own domain has an address record. ⓘ on MAIL says all of
the above in the app's words.

## Who may use the relay

Nobody without a line in `boxes.conf`, and in the first version that file is written by hand:
one box, one line. When a second person wants an address, enrolment is by invite, a one-time
code LocalGhost issues (by hand at first, later for a fee that pays for the machine),
presented with the signed request; for an own domain the request is still refused until the
domain's MX points at the relay, so a code buys one domain its owner controls, or one address
on the shared domain, and nothing else. Without a
line the relay is a wall on every side: receiving for an unenrolled domain is 550 and a bounce
for the sender; every API call needs a token that exists only in a line; each box's queue is
capped. Sending is the part worth guarding, because the relay's IP reputation is shared:
`POST …/out` needs the token, a DKIM signature with the key in the enrolled domain's own DNS, a
`From` on that domain, the allowance, and the relay signs nothing itself, so every message
that leaves is attributable to the domain that sent it and never to `localghost.ai`. Forged
`Received: from mx.localghost.ai` lines and a copied `include:_spf.localghost.ai` gain nothing:
receivers check the IP that actually connected, and only the relay sends from ours.

## The agent and the mail

Mail is the first input the box takes from strangers, and it is data, never instructions.
The chat does not read mail in this version; when it does, a message goes into the prompt in a
quoted block with its sender and verdicts under the rule that nothing inside is a command, and
no action the box can take comes from a message's text alone. The box fetches nothing a message
links to, renders nothing it embeds, and runs nothing it carries. Sending is only ever a tap.

## Later layers, each its own decision

- **End to end.** An OpenPGP key for the address published by WKD
  (`https://<domain>/.well-known/openpgpkey/hu/<hash>`), so Thunderbird, Proton and Mailvelope
  encrypt to it before the sender's provider sees the text and the relay seals ciphertext it
  could not read even in flight; the box decrypts behind the front door (`x/crypto/openpgp`,
  frozen but complete for a key we hold). **Ghost to ghost** needs no cooperation: a box
  writing to another box's domain finds `_ghost.<domain>` TXT with the other box's `mail.pub`,
  seals the whole message to it, and sends an ordinary mail whose body is the sealed blob.
- **The chat reading mail**, the journal mentioning it, searchd indexing it, cued raising a cue
  for a sender in `people`; images into framed's spool as frames with the mail as source.
- Greylisting on; DANE when the domain has DNSSEC; MTA-STS on the box domains (the owner's
  policy file); the `/enrol` endpoint with invites.

## Tests

`internal/mailseal` (round trip at chunk boundaries, the wrong key, truncation, tampering, a
backup is not a mail), `internal/mailauth` (RFC 6376's canonicalisation vectors, `b=`
stripping, sign and verify with both algorithms through the DNS records the box prints, a
relay's `Received` on top, LF line endings, a changed body and a changed subject, no key, DNS
off, SPF cases incl. include, mx, redirect, a macro, a loop and two records, DMARC alignment
strict and relaxed with the org-domain fallback, the header screen, the spam score),
the relay's own tests in the web repository (an SMTP session end to end over loopback TLS: MAIL
before TLS refused, a relay attempt 550, dot-stuffing, the Received line, the note, nothing
readable on disk, an oversize message refused after DATA; path parsing; the API's auth, listing,
fetch and delete; boxes.conf; the structural check; the allowance), `cmd/ghost.noted` (a multipart message with
encoded words, Latin-1 quoted-printable, an HTML alternative, a PDF kept and an executable
named and not kept; HTML reduction; the dangerous-file rules; build, sign, parse back and verify
against the DNS records; reply quoting; address cleaning), `internal/hw` (the mail rows against
Postgres), and the app's `MailTextTest`.
