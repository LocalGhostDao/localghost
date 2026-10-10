# The mirror, run by a daemon

A specification, written 10 October 2026, for the mirror's own server and the daemon that
publishes it. Nothing here is built yet. [WEATHER.md](WEATHER.md), the forecast the box
computes from the weather centres' grids, pulls those grids on the box and does not go
through the mirror; it is mentioned below only as a set the mirror could carry one day.

## What the mirror is today

The mirror is `https://www.localghost.ai/mirror`, a directory of builds published by
`deploy/mirror/publish.sh` in the web repository, run by hand on the machine that deploys the
site. It proxies files exactly as their upstreams publish them (GeoNames, Natural Earth, the OSM
land polygons, Go, llama.cpp, the model weights, the Copernicus tiles, the time zones, the
speech models, LocalGhost's own releases) with one exception, the Copernicus tiles packed by a
pinned `ghost-heights` into sixty files. Every build is a directory named by its moment
(`20261010T003338Z`), immutable once published, with a `MANIFEST.txt` of SHA-256 lines signed by
the site key; the last two builds are kept. A box takes a file with `tools/mirror_fetch.sh`,
which pins the site key's fingerprint, refuses a build older than the one it last used, checks
every byte against the manifest and never falls back to an upstream. That contract is the one
thing this document does not change.

What is manual, and what the daemon takes over, is everything behind the manifest: noticing an
upstream moved, fetching it, packing, writing the manifest, signing, switching the build,
pruning, and saying what state it is all in.

## The server

One Debian 13 machine of its own, the mirror's and nothing else's, reached as
`www.localghost.ai/mirror` through the site's nginx (a `proxy_pass` of `/mirror/` to the
mirror server, so no box changes its base URL), and later as `mirror.localghost.ai` directly
once a release has moved the boxes' default (`GHOST_MIRROR` already overrides it). Disk is
the sizing question, the cache and two builds of every set together: the heights' sixty packs
are 67 GB, the speech and weight sets some 20 GB; 500 GB is comfortable.

The box's own setup scripts make the server, in the manner of a box (`server_setup_root.sh`):
a `ghost-mirror` user, `/var/lib/ghost-mirror`, nginx serving `/var/lib/ghost-mirror/builds`
with `Range` on, directory listings off, `Cache-Control: immutable` on build directories,
`no-store` on `current`, and no access log for the mirror paths, as the privacy page promises
(the mirror keeps no log of which box fetched what).

## ghost.mirrord

A Go daemon in this repository, `cmd/ghost.mirrord`, standard library only like the rest,
one process under systemd, the same shape as the box's daemons (a `ctl` socket, a log a day
under `logs/`, a `status` command). It is the box's `tools/update.sh` turned around: where
the box pulls sets from the mirror, the daemon pulls sets from their upstreams and lays them
out for the boxes.

### The state directory

    /var/lib/ghost-mirror/
      mirror.conf           the sets, as the web repository's file today, carried over
      keys/                 the signing subkey (gpg home, 0700), upstream API keys, each 0600
      cache/                every upstream file ever fetched, by set, named as upstream names it
      cache/pack/<set>/     files the daemon made (heights packs, weather packs)
      builds/<moment>/      a build, hard links into the cache, its MANIFEST.txt and .asc
      builds/current -> <moment>
      state/                last check per set, last change, the newest build used by a box's
                            standard (the build marker), the status the daemon reports
      logs/

A build is hard links, so two builds of the heights cost one copy; pruning a build frees
nothing a newer build still links.

### The sets

`mirror.conf` keeps its format (one line per set, `set=`, `url=` or `list=`, `every=`, `pin=`,
`pack=`, `terms=`), with one addition: `every=` takes `1h` as well as `daily`, `weekly`,
`monthly`, for the release sets. The one made set stays the heights, packed by the pinned
`ghost-heights` out of the `server` set's bundle, so what the daemon makes is reproducible
from the same inputs by the same release.

| set | from | every | kind |
|---|---|---|---|
| geo | GeoNames dumps, Natural Earth | monthly | proxied |
| landpolygons | OSM land polygons | monthly | proxied |
| elevation | Copernicus DEM tiles | monthly check, repacked on a new tool | packed |
| tz | the time zone set | monthly | proxied |
| go | go.dev, checked against its checksums page | weekly | proxied, pinned |
| llama | the tagged llama.cpp tarball | on a new pin | proxied, pinned |
| models, embeddings, speech | the weight files | on a new pin | proxied, pinned |
| server, app | the newest GitHub release of this repository | hourly check | proxied, pinned by the release's SHA256SUMS |

A pinned set changes only when its pin line changes, which is a commit to `mirror.conf` on
the server (the daemon re-reads the file); the `server` and `app` sets follow the release tag
once its `SHA256SUMS.asc` verifies against the site key, so a cut reaches the mirror within the
hour with no hand on the mirror at all.

### The loop

One pass an hour, and on `ghost-mirror-cli publish`:

1. For every set that is due, ask upstream what is there (a HEAD, a listing, a checksums page,
   a release's JSON), compare with `state/`, and fetch what changed into the cache, resumed
   `.part` downloads, each file hashed as it lands. Upstream is TLS only, by an allowlist of
   hosts in the conf; a redirect off the allowlist fails the file. A pinned file whose hash
   is not the pin is deleted and the set reported, never published.
2. Pack the heights when a tile or the tool changed.
3. If anything changed, lay a build: hard links from the cache into `builds/<moment>/`, the
   terms and notice files beside each set, `MANIFEST.txt` written over every file, the build's
   moment in its header; sign it; verify the signature and every hash back from the build
   directory as `mirror_fetch.sh` would (the daemon carries a copy of the check); then switch
   `current` atomically. A build that fails any check is left unlinked and reported; `current`
   stays.
4. Prune: keep the newest two builds and whatever is younger than a day; a build a box might
   be mid-fetch on (younger than the longest fetch, the heights' hour) is never pruned.
5. Write `state/status.json`.

Nothing in the pass needs a person. A set that has not changed costs a HEAD.

### Signing

Today the site key's private half is wherever `publish.sh --sign` runs, under a passphrase a
person types. A daemon that signs four builds a day cannot have that key, and a passphrase on
a key a daemon uses unattended protects nothing, since whatever can read the key file can read
the passphrase the daemon would have to keep beside it. So the daemon never has the site key.
It has a signing subkey of it, made offline with the primary, exported alone
(`--export-secret-subkeys`) with its passphrase removed on the copy, into `keys/` owned by
`ghost-mirror` at 0600, two years' expiry and a calendar entry to replace it. The primary stays
where it is, with its passphrase, and is used by hand for two things only: making a subkey and
revoking one. `mirror_fetch.sh` already accepts a `VALIDSIG` whose primary fingerprint is the
pinned one, so every box on 0.0.2 or later verifies a subkey's signature with the pin it has;
no box changes. A server lost or doubted: the subkey is revoked by the primary, the revocation
goes into `/.well-known/pgp-key.asc`, a new subkey into `keys/`, the daemon restarted; a box
that fetched a build signed by the revoked subkey before the revocation took the same bytes it
would have anyway (the build is checked against the manifest the mirror served, and a thief
with the server has the files, not the boxes), and a box with the updated key file refuses
the old signature from then on.

If a key with no passphrase on the server is not wanted, the other shape is the box's own:
the subkey keeps its passphrase, the mirror starts locked, and `ghost-mirror-cli unlock` asks
for it once per boot and hands it to a gpg agent with no cache limit; until then the daemon
fetches and packs but lays no build and says so in its status. A reboot is rare on a server
that does one thing, so the cost is a prompt a few times a year; the gain is a key that is
useless on a stolen disk. Both are supported by the same daemon (a `keys/locked` marker); the
first is proposed as the default, the second is a one-line choice at setup.

The release signatures (`SHA256SUMS.asc` on GitHub) stay with the site key by hand until the
offline release key that the known gaps promise exists; the mirror signs manifests, not
releases.

### What it says

`ghost-mirror-cli status` and `status.json` (served as `/mirror/status.json`, the one file
outside a build): per set, when it was last checked, when it last changed, the upstream's
answer, the file count and bytes; the current build's moment and age; whether a build is in
progress and at which step; the last error with its time. A human page, `/mirror/`, is the
same in the site's voice (the mirror page the site has, fed from the JSON rather than by
hand). The daemon sends nothing anywhere; the person reads the page, and the box's
INTEGRATIONS page, which already reads the manifest for the newest release, can show the
mirror's age in its SERVER card.

### Failure

Upstream down: the set keeps its last files, the status says so, the build goes ahead without
it. Disk short: the pass stops before a build, with the number. The signing key missing or
expired: no build, loud status; `current` serves on. The daemon restarting: a half-laid
build directory without its manifest is removed at start.

## From publish.sh to the daemon

1. The daemon runs on the new server beside the old process for two weeks, its builds
   compared to `publish.sh`'s, line for line of the manifest; the proxied sets must match
   byte for byte, the heights packs too (same tool, same tiles, same bytes).
2. nginx on www proxies `/mirror/` to the new server. The boxes notice nothing but a newer
   build.
3. `publish.sh` stays in the web repository as the hand-run path with the site key for the
   day the daemon's subkey is revoked; `mirror.conf` moves to the server and the web
   repository's copy is retired.
4. Later, if the fleet's own pulls of the weather grids become a weight on the centres, the
   reduced runs of WEATHER.md are published as a `weather` set made by the same tool, every
   six hours; the box's reader is written so only the source changes.

## What is decided, what is open

Decided: the box contract is untouched; the daemon makes only what a pinned tool from the
`server` set makes; the primary key never sits on a server; the mirror keeps no log of boxes;
the weather is not the mirror's.

Open: the host name's move (`mirror.localghost.ai`) and when the boxes' default follows;
whether `status.json` is signed (it is outside the manifest by design, a box never acts on
it); which of the two signing shapes is the default.
