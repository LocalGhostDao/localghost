LocalGhost drop, 3 October 2026: the weather moves to the box (baseline: the tree at the 0.0.2 cut).

Unzip over the repo root (localghost/, the one with server/ and app/ in it):
    unzip -o localghost-drop.zip -d /home/coder/localghost/localghost
Or apply the diffs from the repo root: server.diff, app.diff, root.diff (git apply, or patch -p0).

Nothing is removed. The version is bumped to 0.0.3 everywhere the cut reads it, so the next cut is
    ./tools/cut_release.sh 0.0.3        (as coder, from server/, after committing)
