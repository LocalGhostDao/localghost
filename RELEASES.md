# Releases

Each release has a name (`server/tools/release.names`), its notes (`server/releases/<version>.md`:
what it does, what is in it, how it works) and a pin (`server/releases/pins.txt`: the commit it
was cut from). `server/tools/cut_release.sh <version>` cuts it, or cuts it again from the same
commit, to the same bytes. The name is the line and the number is the cut: every 0.x is a wisp,
1.x will be shade, and the ghosts grow from there (specter, phantom, poltergeist).

| version | name | date | notes |
|---|---|---|---|
| 0.0.1 | wisp | 2 October 2026 | [server/releases/0.0.1.md](server/releases/0.0.1.md) |
| 0.0.2 | wisp | 2 October 2026 | [server/releases/0.0.2.md](server/releases/0.0.2.md) |
| 0.0.3 | wisp | 3 October 2026 | [server/releases/0.0.3.md](server/releases/0.0.3.md) |
| 0.0.4 | wisp | 4 October 2026 | [server/releases/0.0.4.md](server/releases/0.0.4.md) |
