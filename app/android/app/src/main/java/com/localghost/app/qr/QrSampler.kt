package com.localghost.app.qr

import kotlin.math.abs
import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * Turns a grayscale image (row-major luminance, 0=black..255=white) into a QR module grid that
 * QrMatrixDecode can read. This is the image-processing half of scanning: binarise, find the three
 * finder patterns, recover the perspective, and sample each module's centre.
 *
 * This is a from-scratch detector (no scanning library, by design). It does three things real readers
 * do, so it can read a code that is glare-lit, blurred, or tilted rather than only a clean square-on
 * one: a LOCAL block binariser (a per-region threshold beats an uneven glare gradient that a single
 * global threshold cannot), finder detection with ratio TOLERANCE and a vertical cross-check (modules
 * are not exactly 1:1:3:1:1 once blurred), and a PERSPECTIVE transform from the three finder centres
 * plus a recovered fourth corner (so a tilted code is sampled as if seen square-on).
 *
 * It will not match a mature library's hit rate on hard frames, and it does not try to. If detection
 * fails, scanning returns null and the user types the values instead; a bad sample never produces a
 * wrong enrolment because the decoded fingerprint must still match.
 */
object QrSampler {

    /** A finder-pattern centre in IMAGE pixel coordinates (the analysis frame's space, pre-rotation). */
    data class FinderPoint(val x: Int, val y: Int)

    /**
     * The result of a successful sample: the module grid for decoding, plus the three finder-pattern
     * centres so the UI can draw an overlay anchored to the actual code. Plain class, not a data class,
     * because it holds an Array (a data class would want a custom equals/hashCode it never uses).
     */
    class Sampled(
        val grid: Array<BooleanArray>,
        val finders: List<FinderPoint>,
        val conf: Array<IntArray> = emptyArray(),
    )

    /**
     * Diagnostic: the four outer corners of the QR we locked onto, in analysis-frame image space, in
     * order TL, TR, BR, BL (module coords (0,0),(n,0),(n,n),(0,n) mapped through the sampling transform).
     * The UI reads this to draw the detected boundary on the preview so we can see whether the geometry
     * is right. Remove with the rest of the scan diagnostics before release.
     */
    object ScanGeom {
        @Volatile var corners: List<FinderPoint>? = null
        @Volatile var frameW: Int = 0
        @Volatile var frameH: Int = 0
        @Volatile var rotation: Int = 0
        // Diagnostics surfaced on screen: the size of the best sampled grid (so an unstable version
        // estimate is visible) and whether the bottom-right alignment pattern was actually found (so we
        // can tell if the accurate-sampling path is engaging on dense codes).
        @Volatile var gridN: Int = 0
        @Volatile var alignFound: Boolean = false
        // How many finder-pattern clusters the best binarisation pass saw this frame, even when too few
        // (or too weak) to sample a grid. Cleared each frame alongside corners. The analyser uses it to
        // open the fast-sampling window: a marginal code at distance often shows finders for many frames
        // before a grid ever samples, and those are exactly the frames that need more attempts per second.
        @Volatile var findersSeen: Int = 0
        // Estimated module size in PIXELS from the last finder triple (finder width / 7). A distance
        // proxy the UI turns into "move closer": below ~3px/module the binariser cannot resolve modules
        // reliably, which is exactly when a dense code refuses to decode however steady you hold it.
        @Volatile var moduleLenPx: Double = 0.0
        // Consecutive analyser passes that saw finders but decoded nothing. tryDecode increments it and
        // zeroes it on any successful decode; the UI reads it to decide whether to coach the person.
        @Volatile var noDecodeStreak: Int = 0
    }

    /**
     * Diagnostic outcome of a sample attempt, for on-screen debugging of why a real camera frame is
     * not decoding. Names the stage reached and what it saw.
     */
    sealed interface Diag {
        val note: String
        data class NoBinary(override val note: String) : Diag
        data class FewFinders(val found: Int, override val note: String) : Diag
        data class NoGrid(val finders: Int, override val note: String) : Diag
        data class GridOk(val n: Int, override val note: String) : Diag
    }

    /**
     * Produce several candidate grids (different versions, with and without the alignment anchor) for
     * the caller's decoder to validate. The image pipeline cannot tell a subtly-wrong sampling from a
     * right one , only the decoder can, because only the decoder checks the format BCH and runs Reed
     * Solomon. So instead of the sampler GUESSING the single best grid by timing score (which lets a
     * plausible-but-undecodable grid win), it hands back the plausible grids and lets the decoder be
     * the judge: the first candidate that actually decodes is correct by construction. This is what
     * makes tilted real-world frames work , the timing-best grid and the decodable grid are not always
     * the same one, and only Reed Solomon settles it.
     *
     * Candidates are ordered best-first (timing score), so the common case decodes on the first try.
     */
    fun sampleCandidates(lum: IntArray, width: Int, height: Int): Pair<List<Sampled>, Diag> {
        if (lum.size < width * height) {
            return emptyList<Sampled>() to Diag.NoBinary("frame too small ${width}x${height}")
        }
        // STICKY THRESHOLD + ROTATING PROBE. The pure rotation (one bias per frame, cycling five)
        // fixed the stalled-analyser problem but regressed DETECTION: a code that only resolves
        // under one particular bias got a shot every FIFTH frame, so lock-on felt five times
        // slower. The repair keeps both properties: remember the bias that most recently produced
        // finder candidates and run it EVERY frame (detection recovers to all-biases quality),
        // while a second, rotating probe pass keeps exploring for a better bias (glare drifts,
        // hands move). Worst case two binarisation passes per frame , bounded, nowhere near the
        // 5x that stalled the feed , and the moment any bias shows promise it goes sticky and the
        // common case is one pass again. A sticky bias that misses 12 straight frames is dropped:
        // the scene changed, stop flogging it.
        val biases = intArrayOf(8, 4, 12, 0, 16)
        stickyBias?.let { sb ->
            val r = candidatesForBias(lum, width, height, sb)
            if (r.first.isNotEmpty()) {
                stickyMisses = 0
                precisionScan = false
                return r
            }
            // Seeing SOME finders means the bias is still right and the hand moved , not a miss.
            val partial = (r.second as? Diag.FewFinders)?.found ?: 0
            // NEAR-MISS -> precision: next frame scans every line (stride 1) , when a finder or
            // two are in view, the extra rows are exactly where the missing one hides.
            precisionScan = partial > 0
            if (partial > 0) stickyMisses = 0 else stickyMisses++
            if (stickyMisses >= 12) { stickyBias = null; stickyMisses = 0 }
        }
        val probe = biases[(frameCounter++ % biases.size + biases.size) % biases.size]
        if (probe == stickyBias) {
            // Do not burn the probe re-running the bias that just missed; advance once more.
            val next = biases[(frameCounter++ % biases.size + biases.size) % biases.size]
            val r = candidatesForBias(lum, width, height, next)
            if (r.first.isNotEmpty()) { stickyBias = next; stickyMisses = 0 }
            return r
        }
        val r = candidatesForBias(lum, width, height, probe)
        if (r.first.isNotEmpty()) { stickyBias = probe; stickyMisses = 0 }
        return r
    }

    // Rotates the binarisation threshold across successive frames (see sampleCandidates); the
    // sticky pair remembers the bias that last produced candidates so it runs every frame.
    private var frameCounter = 0
    private var stickyBias: Int? = null
    private var stickyMisses = 0

    /** One binarisation pass: binarise at the given bias, detect, and produce ordered candidate grids. */
    private fun candidatesForBias(lum: IntArray, width: Int, height: Int, bias: Int): Pair<List<Sampled>, Diag> {
        val bin = binariseLocal(lum, width, height, bias)
        val clusters = finderClusters(bin, width, height)
        // Surface how many finders this pass saw (max across biases within the frame), even when a grid
        // never samples , the analyser reads it to keep the sampling rate high while a code is in view.
        if (clusters.size > ScanGeom.findersSeen) ScanGeom.findersSeen = clusters.size
        var clusters3 = clusters
        if (clusters.size == 2) {
            // TWO-OF-THREE RESCUE , the most common near-miss. Two finders fix the code's scale
            // and orientation up to two hypotheses: the missing corner sits at b + (b-a) rotated
            // ±90° (the two right-angle completions) or , if these two are the diagonal , at the
            // midpoint ± the half-diagonal rotated 90°. Search a tight window at each predicted
            // spot with a LENIENT local check; one hit completes the triple and the decoder,
            // as ever, is the judge of whether we guessed right.
            val a = clusters[0].first; val b = clusters[1].first
            val rescued = rescueThirdFinder(bin, w = width, h = height, a = a, b = b)
            if (rescued != null) {
                clusters3 = clusters + Pair(rescued, 1)
            }
        }
        if (clusters3.size < 3) {
            return emptyList<Sampled>() to Diag.FewFinders(clusters3.size, "finders ${clusters3.size}/3 @bias$bias")
        }
        // A finder-shaped data coincidence (often at the bottom-right corner of a small or rounded code)
        // can out-score a real but weak finder, so the single best triple is sometimes the wrong three
        // points. Rather than gamble on one, take the top few candidate triples and let the DECODER be
        // the judge: only the genuine finder set yields a grid with a clean timing pattern that actually
        // decodes. The cheap timing-based version estimate skips most wrong triples before the costly
        // sampling, so considering several stays affordable. Grids are returned best-timing first.
        val triples = selectFinderTriples(clusters3, MAX_TRIPLES)

        data class Cand(val grid: Array<BooleanArray>, val conf: Array<IntArray>, val score: Int, val roles: Corners, val centrality: Double)
        val cands = ArrayList<Cand>()
        for (triple in triples) {
            if (cands.size >= MAX_GRIDS) break
            val roles = assignCorners(triple) ?: continue
            val moduleLen = estimateModuleSize(bin, width, height, roles)
            if (moduleLen != null && moduleLen > 0) ScanGeom.moduleLenPx = moduleLen
            val nFromTiming = countVersionFromTiming(bin, width, height, roles, moduleLen)
            // Version candidates per triple , the decoder is the judge, same principle as the triples
            // themselves. Timing used to be a single bet, and close to a monitor it loses in a
            // characteristic way: screen moire merges module transitions and the count lands on a
            // SMALLER but legal size (v8 for a v12 code), so the "valid" answer was wrong and the
            // geometry estimate , derived from the 7-module finder width, which blur cannot shrink ,
            // was never consulted. Now both nominate: timing first (right when conditions are good),
            // then the geometry snap and its neighbouring versions. LinkedHashSet keeps that order and
            // drops duplicates; MAX_GRIDS still bounds total sampling cost.
            val sizes = LinkedHashSet<Int>()
            if (nFromTiming in 21..97 && (nFromTiming - 17) % 4 == 0) sizes.add(nFromTiming)
            if (moduleLen != null) {
                val raw = (distD(roles.tl, roles.tr) / moduleLen) + 7.0
                val nGeo = ((raw.roundToInt() - 17) / 4) * 4 + 17
                for (d in intArrayOf(0, -4, 4)) {
                    val nc = nGeo + d
                    if (nc in 21..97 && (nc - 17) % 4 == 0) sizes.add(nc)
                }
            }
            if (sizes.isEmpty()) continue // no usable version for this triple; skip it cheaply
            for (nEst in sizes) {
                if (cands.size >= MAX_GRIDS) break
                val (grid, conf) = sampleAtSize(lum, bin, width, height, roles, nEst) ?: continue
                cands.add(Cand(grid, conf, timingScore(grid, nEst), roles, aimCentrality(roles, width, height)))
            }
        }
        if (cands.isEmpty()) return emptyList<Sampled>() to Diag.NoGrid(clusters.size, "no grid @bias$bias")
        // Best first for decode order. Normalised timing quality (so it doesn't merely favour higher
        // versions, which have more transitions) keeps a genuine finder set ahead of a coincidental one;
        // the DECODER is still the final judge, so a fake that sorts high just fails to decode and the next
        // is tried. But when SEVERAL genuine codes are on screen, they ALL decode, and timing alone lets
        // the winner , and thus the angry-ghost anchor , hop between them frame to frame. The centrality
        // term settles it on the code nearest the frame centre, i.e. the one being pointed at. It is sized
        // to break ties only AMONG genuine codes (whose normalised scores sit within ~0.1 of each other)
        // and can never lift a fake, which cannot decode regardless of where it sorts.
        cands.sortByDescending {
            it.score.toDouble() / (it.grid.size - 17).coerceAtLeast(1) + CENTRALITY_W * it.centrality
        }

        // Record the outline of the best-timing candidate for the live reticle.
        val best = cands.first()
        ScanGeom.gridN = best.grid.size
        run {
            val nn = best.grid.size
            val t = buildTransform(bin, width, height, best.roles, nn)
            val tl = t.map(0.0, 0.0); val tr = t.map(nn.toDouble(), 0.0)
            val br = t.map(nn.toDouble(), nn.toDouble()); val bl = t.map(0.0, nn.toDouble())
            ScanGeom.corners = listOf(
                FinderPoint(tl.x.roundToInt(), tl.y.roundToInt()),
                FinderPoint(tr.x.roundToInt(), tr.y.roundToInt()),
                FinderPoint(br.x.roundToInt(), br.y.roundToInt()),
                FinderPoint(bl.x.roundToInt(), bl.y.roundToInt()),
            )
        }
        val out = cands.map { c ->
            val pts = listOf(
                FinderPoint(c.roles.tl.x.roundToInt(), c.roles.tl.y.roundToInt()),
                FinderPoint(c.roles.tr.x.roundToInt(), c.roles.tr.y.roundToInt()),
                FinderPoint(c.roles.bl.x.roundToInt(), c.roles.bl.y.roundToInt()),
            )
            Sampled(c.grid, pts, c.conf)
        }
        return out to Diag.GridOk(out.first().grid.size, "grids @bias$bias x${cands.size}")
    }

    // Multi-triple bounds. Consider up to MAX_TRIPLES candidate finder sets per frame (cheap: just a
    // geometry score), because a data coincidence can push the genuine triple several places down the
    // ranking and we must still reach it. The timing-based version estimate then skips the wrong ones
    // before the expensive sampling, and MAX_GRIDS caps how many grids we actually build and hand to the
    // decoder, so per-frame cost stays bounded even though we look at many triples.
    private const val MAX_TRIPLES = 12
    private const val MAX_GRIDS = 5

    // Weight of the aim-centrality term when ranking candidate grids for decode order (see the sort in
    // candidatesForBias). Sized to break ties AMONG genuine codes toward the one nearest the frame centre,
    // without outweighing the genuine-vs-fake timing gap , a fake cannot decode regardless, so this only
    // decides which real code wins, and thus which one the reticle and angry ghost sit on, when several
    // codes are in view. Zero would restore the old pure-timing order (anchor free to hop between codes).
    private const val CENTRALITY_W = 0.35

    // How centred a finder triple is in the frame: 1.0 at the exact centre, ~0.0 at a corner. The QR centre
    // is taken as the midpoint of the two diagonal finders (tr, bl), which is close enough to compare codes.
    // Used to settle the anchor on the code being pointed at when several are on screen.
    private fun aimCentrality(roles: Corners, width: Int, height: Int): Double {
        val cx = (roles.tr.x + roles.bl.x) / 2.0
        val cy = (roles.tr.y + roles.bl.y) / 2.0
        val d = hypot(cx - width / 2.0, cy - height / 2.0)
        val halfDiag = hypot(width.toDouble(), height.toDouble()) / 2.0
        return (1.0 - d / halfDiag).coerceIn(0.0, 1.0)
    }

    // ====================================================================================
    // 1. LOCAL BINARISATION (ZXing-style block thresholding)
    //
    // A single global threshold cannot handle an uneven glare gradient: the bright side of the frame
    // pushes the whole threshold up and the dark side floods to black. Instead, split the image into
    // BLOCK x BLOCK tiles, take each tile's average, smooth it against neighbouring tiles, and
    // threshold each pixel against its local tile average. This is the single biggest robustness win.
    // ====================================================================================

    // 5px is small (a code on a screen at arm's length, e.g. a version-5 code), and an 8px tile spans
    // more than one module, so the local mean blends adjacent modules and the threshold smears the
    // finder edges , which is exactly when detection drops the top-right finder. A 5px tile keeps the
    // local mean roughly one module wide, preserving small-code structure, while still being local
    // enough to beat glare and gradients on big codes. The neighbour smoothing below keeps it stable.
    private const val BLOCK = 5

    // Reusable binariser buffers, sized to the frame. The analyser is single-threaded, so reusing these
    // avoids allocating large arrays every frame, which was adding to the garbage-collector load behind
    // the heat and the gradual slowdown.
    private var binOut: BooleanArray = BooleanArray(0)
    private var binBlockMin: IntArray = IntArray(0)
    private var binBlockMax: IntArray = IntArray(0)

    private fun binariseLocal(lum: IntArray, w: Int, h: Int, bias: Int = 8): BooleanArray {
        if (binOut.size != w * h) binOut = BooleanArray(w * h)
        val out = binOut
        val bw = (w + BLOCK - 1) / BLOCK
        val bh = (h + BLOCK - 1) / BLOCK
        if (binBlockMin.size != bw * bh) binBlockMin = IntArray(bw * bh)
        if (binBlockMax.size != bw * bh) binBlockMax = IntArray(bw * bh)
        val blockMin = binBlockMin
        val blockMax = binBlockMax

        // Per-tile min and max, and the global extremes as a fallback. We threshold on the local min/max
        // MIDPOINT rather than the mean: the mean of a window collapses onto a single module's own colour
        // once modules grow larger than the window (which is what happens when the phone moves close), so
        // the classification breaks. The midpoint of the darkest and lightest pixel in the neighbourhood
        // stays correct as long as the window still sees both a dark and a light module.
        var gMin = 255; var gMax = 0
        for (by in 0 until bh) {
            for (bx in 0 until bw) {
                val x0 = bx * BLOCK; val y0 = by * BLOCK
                val x1 = min(x0 + BLOCK, w); val y1 = min(y0 + BLOCK, h)
                var lo = 255; var hi = 0
                var yy = y0
                while (yy < y1) {
                    val rowBase = yy * w
                    var xx = x0
                    while (xx < x1) {
                        val v = lum[rowBase + xx] and 0xFF
                        if (v < lo) lo = v
                        if (v > hi) hi = v
                        xx++
                    }
                    yy++
                }
                blockMin[by * bw + bx] = lo
                blockMax[by * bw + bx] = hi
                if (lo < gMin) gMin = lo
                if (hi > gMax) gMax = hi
            }
        }
        val globalMid = (gMin + gMax) / 2

        // Threshold each tile against the min/max midpoint of its 5x5 tile neighbourhood. When that
        // neighbourhood is nearly uniform (contrast below CONTRAST_MIN), it sits inside one large module
        // or in the quiet zone, so a local threshold is meaningless , fall back to the global midpoint,
        // which still separates dark modules from light ones. This is what lets a code decode when it is
        // held close (large modules) as well as at the usual distance.
        for (by in 0 until bh) {
            for (bx in 0 until bw) {
                var lo = 255; var hi = 0
                val r = 2
                var ny = max(0, by - r)
                while (ny <= min(bh - 1, by + r)) {
                    var nx = max(0, bx - r)
                    while (nx <= min(bw - 1, bx + r)) {
                        val iMin = blockMin[ny * bw + nx]; val iMax = blockMax[ny * bw + nx]
                        if (iMin < lo) lo = iMin
                        if (iMax > hi) hi = iMax
                        nx++
                    }
                    ny++
                }
                val thresh = if (hi - lo >= CONTRAST_MIN) (lo + hi) / 2 - bias else globalMid - bias

                val x0 = bx * BLOCK; val y0 = by * BLOCK
                val x1 = min(x0 + BLOCK, w); val y1 = min(y0 + BLOCK, h)
                var yy = y0
                while (yy < y1) {
                    val rowBase = yy * w
                    var xx = x0
                    while (xx < x1) {
                        out[rowBase + xx] = (lum[rowBase + xx] and 0xFF) < thresh
                        xx++
                    }
                    yy++
                }
            }
        }
        return out
    }

    // Below this dark-to-light spread (out of 255) a tile neighbourhood is treated as uniform , inside a
    // big module or the quiet zone , and thresholded against the global midpoint instead of its own local
    // range, which would otherwise be noise.
    private const val CONTRAST_MIN = 24

    private fun dark(bin: BooleanArray, w: Int, x: Int, y: Int) = bin[y * w + x]

    // ====================================================================================
    // 2. FINDER DETECTION (ratio-tolerant horizontal scan + vertical cross-check)
    //
    // Scan rows for the 1:1:3:1:1 dark/light/dark/light/dark ratio of a finder pattern, with a
    // tolerance (blur smears the boundaries). For each horizontal hit, cross-check vertically through
    // the candidate centre: a real finder also shows ~1:1:3:1:1 down its middle. This rejects the many
    // false 1:1:3:1:1 runs that occur in ordinary data rows. Cluster surviving centres.
    // ====================================================================================

    private data class Pt(val x: Double, val y: Double, val mod: Double = 0.0)

    // Stride 1 when the last pass NEARLY had it (partial finders), stride 2 otherwise , a finder
    // is >= 7 modules tall, so every-2nd-line scanning cannot miss one big enough to decode, and
    // the halved cost is what lets sticky + probe both run every frame without stalling.
    @Volatile var precisionScan = false

    private fun finderClusters(bin: BooleanArray, w: Int, h: Int): List<Pair<Pt, Int>> {
        val step = if (precisionScan) 1 else 2
        val candidates = ArrayList<Pt>()
        for (y in 0 until h step step) {
            var x = 0
            while (x < w) {
                if (dark(bin, w, x, y)) {
                    val runs = IntArray(5)
                    var ri = 0
                    var cx = x
                    var expectDark = true
                    val runStart = x
                    while (cx < w && ri < 5) {
                        var run = 0
                        while (cx < w && dark(bin, w, cx, y) == expectDark) { run++; cx++ }
                        runs[ri++] = run
                        expectDark = !expectDark
                    }
                    if (ri == 5 && matches11311(runs)) {
                        // Sub-pixel horizontal centre: centre of the middle (3-module) dark bar, which
                        // starts after runs[0]+runs[1] from runStart and spans runs[2].
                        val midStart = runStart + runs[0] + runs[1]
                        val centreXd = midStart + runs[2] / 2.0
                        val centreXi = centreXd.toInt()
                        // Confirm it is a real finder via the vertical cross-check, or , for finders
                        // rotated enough to break the vertical scan , via a diagonal cross-check.
                        var centreYd = verticalCentre(bin, w, h, centreXi, y)
                        var refinedX = centreXd
                        if (!centreYd.isNaN()) {
                            val hx = horizontalCentre(bin, w, h, centreXi, centreYd.toInt())
                            if (!hx.isNaN()) {
                                refinedX = hx
                                // One more vertical pass at the refined X converges the centre.
                                val vy2 = verticalCentre(bin, w, h, hx.toInt(), centreYd.toInt())
                                if (!vy2.isNaN()) centreYd = vy2
                            }
                        }
                        if (!centreYd.isNaN()) {
                            val mod = runs.sum() / 7.0
                            candidates.add(Pt(refinedX, centreYd, mod))
                        } else {
                            val d = diagonalFinder(bin, w, h, centreXi, y, 1)
                                ?: diagonalFinder(bin, w, h, centreXi, y, -1)
                            if (d != null) candidates.add(d)
                        }
                    }
                    x = cx
                } else x++
            }
        }
        // Second pass: scan columns for the same 1:1:3:1:1 vertically. A finder whose centre row is
        // clipped by rotation can be missed by the horizontal pass but caught here (and vice versa).
        // Clustering merges the two passes, so a finder seen either way survives.
        for (x in 0 until w step step) {
            var y = 0
            while (y < h) {
                if (dark(bin, w, x, y)) {
                    val runs = IntArray(5)
                    var ri = 0
                    var cy = y
                    var expectDark = true
                    val runStart = y
                    while (cy < h && ri < 5) {
                        var run = 0
                        while (cy < h && dark(bin, w, x, cy) == expectDark) { run++; cy++ }
                        runs[ri++] = run
                        expectDark = !expectDark
                    }
                    if (ri == 5 && matches11311(runs)) {
                        val midStart = runStart + runs[0] + runs[1]
                        val centreYd = midStart + runs[2] / 2.0
                        val centreXd = horizontalCentre(bin, w, h, x, centreYd.toInt())
                        if (!centreXd.isNaN()) {
                            val mod = runs.sum() / 7.0
                            candidates.add(Pt(centreXd, centreYd, mod))
                        } else {
                            val d = diagonalFinder(bin, w, h, x, centreYd.toInt(), 1)
                                ?: diagonalFinder(bin, w, h, x, centreYd.toInt(), -1)
                            if (d != null) candidates.add(d)
                        }
                    }
                    y = cy
                } else y++
            }
        }
        // Pick the three that actually form a QR finder set , similar module size and a right-isosceles
        // triangle , NOT just the three with the most support. A false 1:1:3:1:1 hit in the data (or the
        // bottom-right corner) can out-support a real finder; selecting by support alone then builds the
        // homography on the wrong three points and the whole decode silently fails.
        // Cluster nearby hits into finder candidates (centre + how many scanlines supported it).
        return cluster(candidates)
    }

    /** Horizontal cross-check mirror of verticalCentre: sub-pixel x-centre of the middle bar, or NaN. */
    private fun horizontalCentre(bin: BooleanArray, w: Int, h: Int, cx: Int, cy: Int): Double {
        if (cy < 0 || cy >= h) return Double.NaN
        if (!dark(bin, w, cx, cy)) return Double.NaN
        val runs = IntArray(5)
        var left = cx; while (left > 0 && dark(bin, w, left - 1, cy)) left--
        var right = cx; while (right < w - 1 && dark(bin, w, right + 1, cy)) right++
        runs[2] = right - left + 1
        var a = left - 1; var lenLeftLight = 0
        while (a >= 0 && !dark(bin, w, a, cy)) { lenLeftLight++; a-- }
        runs[1] = lenLeftLight
        var darkLeft = 0
        while (a >= 0 && dark(bin, w, a, cy)) { darkLeft++; a-- }
        runs[0] = darkLeft
        var b = right + 1; var lenRightLight = 0
        while (b < w && !dark(bin, w, b, cy)) { lenRightLight++; b++ }
        runs[3] = lenRightLight
        var darkRight = 0
        while (b < w && dark(bin, w, b, cy)) { darkRight++; b++ }
        runs[4] = darkRight
        if (runs.any { it == 0 }) return Double.NaN
        if (!matches11311(runs)) return Double.NaN
        return (left + right) / 2.0
    }

    /**
     * Diagonal cross-check (ZXing-inspired): confirm the 1:1:3:1:1 ratio along a diagonal through the
     * centre, and if it holds, return the finder centre (midpoint of the diagonal centre run) plus a
     * module size measured along the axis at that centre (so it is comparable to axis-detected finders,
     * not inflated by the diagonal's sqrt(2) length). dir = +1 for '\', -1 for '/'. Returns null if no
     * finder. A finder rotated enough to break the pure horizontal and vertical scans still reads on a
     * diagonal, so this recovers finders the axis cross-checks miss.
     */
    private fun diagonalFinder(bin: BooleanArray, w: Int, h: Int, cx: Int, cy: Int, dir: Int): Pt? {
        if (cx < 0 || cx >= w || cy < 0 || cy >= h) return null
        if (!dark(bin, w, cx, cy)) return null
        val runs = IntArray(5)
        var ux = cx; var uy = cy
        while (ux - 1 in 0 until w && uy - dir in 0 until h && dark(bin, w, ux - 1, uy - dir)) { ux--; uy -= dir }
        var dx = cx; var dy = cy
        while (dx + 1 in 0 until w && dy + dir in 0 until h && dark(bin, w, dx + 1, dy + dir)) { dx++; dy += dir }
        runs[2] = maxOf(abs(dx - ux), abs(dy - uy)) + 1
        var ax = ux - 1; var ay = uy - dir; var l1 = 0
        while (ax in 0 until w && ay in 0 until h && !dark(bin, w, ax, ay)) { l1++; ax--; ay -= dir }
        runs[1] = l1
        var d0 = 0
        while (ax in 0 until w && ay in 0 until h && dark(bin, w, ax, ay)) { d0++; ax--; ay -= dir }
        runs[0] = d0
        var bx = dx + 1; var by = dy + dir; var l3 = 0
        while (bx in 0 until w && by in 0 until h && !dark(bin, w, bx, by)) { l3++; bx++; by += dir }
        runs[3] = l3
        var d4 = 0
        while (bx in 0 until w && by in 0 until h && dark(bin, w, bx, by)) { d4++; bx++; by += dir }
        runs[4] = d4
        if (runs.any { it == 0 }) return null
        if (!matches11311(runs)) return null
        // centre = midpoint of the diagonal centre run.
        val ccx = (ux + dx) / 2.0
        val ccy = (uy + dy) / 2.0
        // module size measured along the vertical axis at the centre, to match axis-detected finders.
        val cxi = ccx.roundToInt(); val cyi = ccy.roundToInt()
        var up = cyi; while (up > 0 && dark(bin, w, cxi, up - 1)) up--
        var down = cyi; while (down < h - 1 && dark(bin, w, cxi, down + 1)) down++
        val centreBar = (down - up + 1)
        val modAxis = centreBar / 3.0 // the centre dark bar is 3 modules
        if (modAxis <= 0.0) return null
        return Pt(ccx, ccy, modAxis)
    }

    /**
     * Enumerate candidate finder triples and return up to k of them ranked best-first, so the caller can
     * try several and let the decoder pick the one that actually decodes. This is how we beat a
     * finder-shaped data coincidence: it may win the geometry score, but it cannot win the decode, and
     * the genuine triple is almost always within the top few here even when it is not ranked first.
     */
    private fun selectFinderTriples(clusters: List<Pair<Pt, Int>>, k: Int): List<List<Pt>> {
        if (clusters.size <= 3) return if (clusters.size == 3) listOf(clusters.map { it.first }) else emptyList()
        val scored = ArrayList<Pair<Double, List<Pt>>>()
        val n = clusters.size
        for (i in 0 until n) for (j in i + 1 until n) for (m in j + 1 until n) {
            val a = clusters[i]; val b = clusters[j]; val c = clusters[m]
            val pa = a.first; val pb = b.first; val pc = c.first
            val mods = doubleArrayOf(pa.mod, pb.mod, pc.mod)
            val mAvg = (mods[0] + mods[1] + mods[2]) / 3.0
            if (mAvg <= 0.0) continue
            val modSpread = (maxOf(mods[0], mods[1], mods[2]) - minOf(mods[0], mods[1], mods[2])) / mAvg
            if (modSpread > 0.5) continue
            val geom = rightIsoscelesScore(pa, pb, pc)
            if (geom > 0.6) continue
            val legPx = legLength(pa, pb, pc)
            val dimEst = legPx / mAvg + 7.0
            // 17..68, NOT 19..60: the enrol sequence uses up to version 11, which is 61 modules ,
            // the old 60 cap rejected every v11 triple at the geometry stage even with all three
            // finders cleanly found (the "bigger QRs" that would not catch). The bounds are an
            // ESTIMATE filter, not a validity check , dimension snapping downstream does the real
            // decision , so they carry slack for perspective and module-size noise on both ends.
            if (dimEst < 17.0 || dimEst > 68.0) continue
            val support = a.second + b.second + c.second
            val score = geom + modSpread * 5.0 - support * 0.01
            scored.add(score to listOf(pa, pb, pc))
        }
        if (scored.isEmpty()) {
            // Nothing passed the finder checks; offer the highest-support triple so the caller can try.
            return listOf(clusters.sortedByDescending { it.second }.take(3).map { it.first })
        }
        scored.sortBy { it.first }
        return scored.take(k).map { it.second }
    }

    /** Right-isosceles fit: 0 when two legs from the best vertex are equal and perpendicular. */
    private fun rightIsoscelesScore(a: Pt, b: Pt, c: Pt): Double {
        var best = Double.MAX_VALUE
        val pts = listOf(a, b, c)
        for (v in 0 until 3) {
            val corner = pts[v]; val p1 = pts[(v + 1) % 3]; val p2 = pts[(v + 2) % 3]
            val e1x = p1.x - corner.x; val e1y = p1.y - corner.y
            val e2x = p2.x - corner.x; val e2y = p2.y - corner.y
            val l1 = kotlin.math.hypot(e1x, e1y); val l2 = kotlin.math.hypot(e2x, e2y)
            if (l1 == 0.0 || l2 == 0.0) continue
            val legRatio = abs(l1 - l2) / maxOf(l1, l2)            // 0 when legs equal
            val cosAng = abs((e1x * e2x + e1y * e2y) / (l1 * l2))  // 0 when perpendicular
            val s = legRatio + cosAng
            if (s < best) best = s
        }
        return best
    }

    /** Length of one finder leg (the top-left vertex's edge) for the best right-angle vertex. */
    private fun legLength(a: Pt, b: Pt, c: Pt): Double {
        var best = Double.MAX_VALUE; var leg = 0.0
        val pts = listOf(a, b, c)
        for (v in 0 until 3) {
            val corner = pts[v]; val p1 = pts[(v + 1) % 3]; val p2 = pts[(v + 2) % 3]
            val l1 = dist(corner, p1); val l2 = dist(corner, p2)
            if (l1 == 0.0 || l2 == 0.0) continue
            val legRatio = abs(l1 - l2) / maxOf(l1, l2)
            val e1x = p1.x - corner.x; val e1y = p1.y - corner.y
            val e2x = p2.x - corner.x; val e2y = p2.y - corner.y
            val cosAng = abs((e1x * e2x + e1y * e2y) / (l1 * l2))
            val s = legRatio + cosAng
            if (s < best) { best = s; leg = (l1 + l2) / 2.0 }
        }
        return leg
    }

    /** Cross-check: does column centerX, around row y, also read ~1:1:3:1:1 vertically? */
    private fun verticalCentre(bin: BooleanArray, w: Int, h: Int, cx: Int, cy: Int): Double {
        if (cx < 0 || cx >= w) return Double.NaN
        if (!dark(bin, w, cx, cy)) return Double.NaN
        // walk up and down collecting runs centred on (cx, cy)
        val runs = IntArray(5)
        var up = cy; while (up > 0 && dark(bin, w, cx, up - 1)) up--
        var down = cy; while (down < h - 1 && dark(bin, w, cx, down + 1)) down++
        runs[2] = down - up + 1
        var a = up - 1; var lenLightTop = 0
        while (a >= 0 && !dark(bin, w, cx, a)) { lenLightTop++; a-- }
        runs[1] = lenLightTop
        var darkTop = 0
        while (a >= 0 && dark(bin, w, cx, a)) { darkTop++; a-- }
        runs[0] = darkTop
        var b = down + 1; var lenLightBot = 0
        while (b < h && !dark(bin, w, cx, b)) { lenLightBot++; b++ }
        runs[3] = lenLightBot
        var darkBot = 0
        while (b < h && dark(bin, w, cx, b)) { darkBot++; b++ }
        runs[4] = darkBot
        if (runs.any { it == 0 }) return Double.NaN
        if (!matches11311(runs)) return Double.NaN
        // sub-pixel centre of the middle dark bar: midpoint of [up, down].
        return (up + down) / 2.0
    }

    private fun matches11311(r: IntArray): Boolean {
        val total = r.sum()
        if (total < 7) return false
        // Judge the centre against the ARMS, not a global unit. The four arms (r0,r1,r3,r4) should share
        // one width; the centre (r2) must be a genuinely WIDE bar, about 3x an arm. The old global-unit
        // test with a wide tolerance let a 1:1:1:1:1 alignment pattern (centre == arm) pass as a finder,
        // which is the bottom-right impostor that out-competed the real top-right finder. Requiring the
        // centre to be at least ~2x the arm rejects the alignment pattern while still accepting a blurred
        // real finder (centre 2.5-3.5x), and keeping the arms near-equal rejects ragged data coincidences.
        val arm = (r[0] + r[1] + r[3] + r[4]) / 4.0
        if (arm < 1.0) return false
        // SMALL-MODULE leniency: at a distance a module is 1-2px and integer run lengths make the
        // arm ratios inherently ragged (a 1px vs 2px arm is a 100% "error" that means nothing).
        // Below ~2.5px arms the equality tolerance widens and the centre floor drops slightly ,
        // measured against the far-QR misses, not guessed.
        val tol = if (arm < 2.5) 0.85 else 0.6
        for (v in intArrayOf(r[0], r[1], r[3], r[4])) {
            if (abs(v - arm) > tol * arm) return false      // arms must be roughly equal
        }
        val lo = if (arm < 2.5) 1.7 else 2.0
        if (r[2] < lo * arm || r[2] > 4.5 * arm) return false   // centre must be a real wide bar
        return true
    }

    /** Group nearby points; return cluster centroids (with averaged module size) and member counts. */
    private fun cluster(pts: List<Pt>): List<Pair<Pt, Int>> {
        val out = ArrayList<Pair<Pt, Int>>()
        val sumX = ArrayList<Double>(); val sumY = ArrayList<Double>()
        val sumM = ArrayList<Double>(); val cnt = ArrayList<Int>()
        for (p in pts) {
            var hit = -1
            for (i in out.indices) {
                val c = out[i].first
                // MODULE-PROPORTIONAL radius, floored at the old 14px. A fixed radius was the
                // big-code detection bug: a code filling the frame has ~19px modules, its finder is
                // ~133px across, and the row scan yields candidates spanning the whole ~3-module
                // core , a 14px radius SHATTERED one physical finder into a vertical stack of four
                // or five clusters, and the triple selector drowned in fragments of the same corner.
                // 3.5 modules covers a finder's candidate spread at any scale; small codes keep the
                // 14px floor and behave exactly as before.
                val radius = maxOf(14.0, 3.5 * maxOf(c.mod, p.mod))
                if (abs(c.x - p.x) <= radius && abs(c.y - p.y) <= radius) { hit = i; break }
            }
            if (hit >= 0) {
                sumX[hit] = sumX[hit] + p.x; sumY[hit] = sumY[hit] + p.y
                sumM[hit] = sumM[hit] + p.mod; cnt[hit] = cnt[hit] + 1
                out[hit] = Pt(sumX[hit] / cnt[hit], sumY[hit] / cnt[hit], sumM[hit] / cnt[hit]) to cnt[hit]
            } else {
                out.add(p to 1); sumX.add(p.x); sumY.add(p.y); sumM.add(p.mod); cnt.add(1)
            }
        }
        return out.filter { it.second >= 2 }
    }

    // ====================================================================================
    // 3. CORNER ROLES + PERSPECTIVE SAMPLING
    //
    // From three finder centres, identify which is top-left (the corner), top-right, and bottom-left
    // by geometry: the top-left is the vertex of the near-right-angle. Estimate the module size from a
    // finder, derive the version, recover the fourth (bottom-right) corner, and sample every module
    // centre through a perspective transform fitted to the four corners.
    // ====================================================================================

    private class Corners(
        val tl: DoublePt, val tr: DoublePt, val bl: DoublePt,
    )
    private data class DoublePt(val x: Double, val y: Double)

    /**
     * A 3x3 projective (perspective) transform mapping module-space coordinates to image pixels.
     * Inspired by ZXing's PerspectiveTransform (Apache-2.0); ported, not copied. A phone photo of a QR
     * has perspective, so a bilinear/affine map drifts toward the far corner and mis-samples modules.
     * A homography built from four point correspondences is exact for any planar view at any angle,
     * which is what lets a skewed real-camera frame sample cleanly. Credit: the ZXing project.
     */
    private class PerspectiveTransform private constructor(
        val a11: Double, val a21: Double, val a31: Double,
        val a12: Double, val a22: Double, val a32: Double,
        val a13: Double, val a23: Double, val a33: Double,
    ) {
        fun map(x: Double, y: Double): DoublePt {
            val denom = a13 * x + a23 * y + a33
            return DoublePt(
                (a11 * x + a21 * y + a31) / denom,
                (a12 * x + a22 * y + a32) / denom,
            )
        }

        companion object {
            fun quadToQuad(
                x0: Double, y0: Double, x1: Double, y1: Double,
                x2: Double, y2: Double, x3: Double, y3: Double,
                x0p: Double, y0p: Double, x1p: Double, y1p: Double,
                x2p: Double, y2p: Double, x3p: Double, y3p: Double,
            ): PerspectiveTransform {
                val qToS = squareToQuad(x0, y0, x1, y1, x2, y2, x3, y3).buildAdjoint()
                val sToQ = squareToQuad(x0p, y0p, x1p, y1p, x2p, y2p, x3p, y3p)
                return sToQ.times(qToS)
            }

            private fun squareToQuad(
                x0: Double, y0: Double, x1: Double, y1: Double,
                x2: Double, y2: Double, x3: Double, y3: Double,
            ): PerspectiveTransform {
                val dx3 = x0 - x1 + x2 - x3
                val dy3 = y0 - y1 + y2 - y3
                if (dx3 == 0.0 && dy3 == 0.0) {
                    return PerspectiveTransform(
                        x1 - x0, x2 - x1, x0,
                        y1 - y0, y2 - y1, y0,
                        0.0, 0.0, 1.0,
                    )
                }
                val dx1 = x1 - x2; val dx2 = x3 - x2
                val dy1 = y1 - y2; val dy2 = y3 - y2
                val denom = dx1 * dy2 - dx2 * dy1
                val a13v = (dx3 * dy2 - dx2 * dy3) / denom
                val a23v = (dx1 * dy3 - dx3 * dy1) / denom
                return PerspectiveTransform(
                    x1 - x0 + a13v * x1, x3 - x0 + a23v * x3, x0,
                    y1 - y0 + a13v * y1, y3 - y0 + a23v * y3, y0,
                    a13v, a23v, 1.0,
                )
            }
        }

        private fun buildAdjoint(): PerspectiveTransform = PerspectiveTransform(
            a22 * a33 - a23 * a32, a23 * a31 - a21 * a33, a21 * a32 - a22 * a31,
            a13 * a32 - a12 * a33, a11 * a33 - a13 * a31, a12 * a31 - a11 * a32,
            a12 * a23 - a13 * a22, a13 * a21 - a11 * a23, a11 * a22 - a12 * a21,
        )

        private fun times(o: PerspectiveTransform): PerspectiveTransform = PerspectiveTransform(
            a11 * o.a11 + a21 * o.a12 + a31 * o.a13,
            a11 * o.a21 + a21 * o.a22 + a31 * o.a23,
            a11 * o.a31 + a21 * o.a32 + a31 * o.a33,
            a12 * o.a11 + a22 * o.a12 + a32 * o.a13,
            a12 * o.a21 + a22 * o.a22 + a32 * o.a23,
            a12 * o.a31 + a22 * o.a32 + a32 * o.a33,
            a13 * o.a11 + a23 * o.a12 + a33 * o.a13,
            a13 * o.a21 + a23 * o.a22 + a33 * o.a23,
            a13 * o.a31 + a23 * o.a32 + a33 * o.a33,
        )
    }


    private fun assignCorners(centres: List<Pt>): Corners? {
        if (centres.size < 3) return null
        // take the three strongest clusters
        val a = centres[0]; val b = centres[1]; val c = centres[2]
        val pts = listOf(a, b, c)
        // top-left is the point opposite the longest side (the hypotenuse of the finder L)
        val dAB = dist(a, b); val dAC = dist(a, c); val dBC = dist(b, c)
        val tl: Pt; val p1: Pt; val p2: Pt
        when (maxOf(dAB, dAC, dBC)) {
            dAB -> { tl = c; p1 = a; p2 = b }
            dAC -> { tl = b; p1 = a; p2 = c }
            else -> { tl = a; p1 = b; p2 = c }
        }
        // Of the two remaining points, assign top-right and bottom-left with a RIGHT-HANDED winding in
        // image coordinates (y points down). tl is the finder L-vertex, i.e. the true top-left. For a
        // correctly-oriented QR the signed cross (tl->tr) x (tl->bl) is positive: tr is to the right
        // (+x), bl is down (+y), and (+x,0) x (0,+y) > 0. So the assignment whose cross is positive is
        // the physically correct, non-mirrored one. The previous code tested the wrong sign, which built
        // a reflected grid that no rotation could fix and that the decoder therefore never read.
        val v1 = DoublePt(p1.x - tl.x, p1.y - tl.y)
        val v2 = DoublePt(p2.x - tl.x, p2.y - tl.y)
        val cross = v1.x * v2.y - v1.y * v2.x
        val (tr, bl) = if (cross > 0) p1 to p2 else p2 to p1
        return Corners(
            DoublePt(tl.x, tl.y),
            DoublePt(tr.x, tr.y),
            DoublePt(bl.x, bl.y),
        )
    }

    /**
     * Count the QR dimension (n modules per side) by walking the line between the two top finder
     * centres and counting dark/light transitions. The span from TL centre to TR centre is exactly
     * (n-7) modules. The median gap between transitions along that line (in the 0..1 line parameter)
     * equals one module in the timing region, so 1/medianGap gives (n-7) directly , far steadier than
     * estimating one finder's width, because it is a count of real module boundaries rather than a
     * pixel-size division. Returns 0 if it cannot read the line cleanly (caller falls back).
     */
    private fun countVersionFromTiming(bin: BooleanArray, w: Int, h: Int, c: Corners, moduleLen: Double?): Int {
        if (moduleLen == null || moduleLen <= 0.0) return 0
        // The timing pattern is at module row 6, but the finder centres (c.tl, c.tr) sit at row 3.5, so the
        // straight tl->tr line runs through the DATA at row 3.5. Data has runs of same-colour modules, so
        // its transition gaps are too wide and the module count comes out far too low (a v9 measured this
        // way reads as v3-v8). Offset the measuring line perpendicular, toward the interior, by ~2.5
        // modules to land on the real timing pattern, whose every-module alternation gives an honest count.
        // Try a few offsets spanning row 6 and keep the largest module count: that is the row that actually
        // is the timing pattern (maximum transition density), robust to a small error in the module size.
        val dx = c.tr.x - c.tl.x; val dy = c.tr.y - c.tl.y
        val len = kotlin.math.hypot(dx, dy)
        if (len <= 0.0) return 0
        var px = -dy / len; var py = dx / len
        val toBlX = c.bl.x - c.tl.x; val toBlY = c.bl.y - c.tl.y
        if (px * toBlX + py * toBlY < 0) { px = -px; py = -py }

        var bestSpan = 0
        for (offMod in doubleArrayOf(2.0, 2.5, 3.0)) {
            val ox = px * offMod * moduleLen; val oy = py * offMod * moduleLen
            val a = DoublePt(c.tl.x + ox, c.tl.y + oy)
            val b = DoublePt(c.tr.x + ox, c.tr.y + oy)
            val span = countModulesAlong(bin, w, h, a, b)
            if (span > bestSpan) bestSpan = span
        }
        if (bestSpan <= 0) return 0
        val n = bestSpan + 7
        // snap to nearest valid QR size (17 + 4v). round-to-nearest, not biased.
        val v = ((n - 17).toDouble() / 4.0).roundToInt()
        val snapped = 17 + 4 * v
        return if (snapped in 21..97) snapped else 0
    }

    /** Modules along a line, from the median gap between colour transitions (1/medianGap). 0 if too few. */
    private fun countModulesAlong(bin: BooleanArray, w: Int, h: Int, a: DoublePt, b: DoublePt): Int {
        val steps = 1000
        val transitions = ArrayList<Double>()
        var prev = sampleLine(bin, w, h, a, b, 0.0) ?: return 0
        for (i in 1..steps) {
            val t = i.toDouble() / steps
            val v = sampleLine(bin, w, h, a, b, t) ?: continue
            if (v != prev) transitions.add(t)
            prev = v
        }
        if (transitions.size < 6) return 0
        val gaps = ArrayList<Double>()
        for (i in 1 until transitions.size) gaps.add(transitions[i] - transitions[i - 1])
        gaps.sort()
        val medianGap = gaps[gaps.size / 2]
        if (medianGap <= 0.0) return 0
        return (1.0 / medianGap).roundToInt()
    }

    /** Sample the binary image along the line from a to b at parameter t in [0,1]. Null if off-frame. */
    private fun sampleLine(bin: BooleanArray, w: Int, h: Int, a: DoublePt, b: DoublePt, t: Double): Boolean? {
        val x = (a.x + (b.x - a.x) * t).roundToInt()
        val y = (a.y + (b.y - a.y) * t).roundToInt()
        if (x < 0 || x >= w || y < 0 || y >= h) return null
        return dark(bin, w, x, y)
    }

    /**
     * Sample a grid of exactly n modules using a perspective transform (homography) built from the
     * three finder centres plus, when found, the bottom-right alignment pattern. This replaces the old
     * bilinear map, which was only correct for a flat-on view and drifted under perspective. Inspired
     * by ZXing's grid sampler (Apache-2.0), ported not copied. Null if any sample goes off-frame.
     */
    private fun sampleAtSize(lum: IntArray, bin: BooleanArray, w: Int, h: Int, c: Corners, n: Int): Pair<Array<BooleanArray>, Array<IntArray>>? {
        val transform = buildTransformRefined(bin, w, h, c, n)
        return sampleWithTransform(lum, w, h, n, transform)
    }

    /**
     * Sample an n-module grid through a transform. Returns the boolean grid AND a per-module confidence
     * (0 = the supersampled cell was an even split, ambiguous; s*s = unanimous). A logo, glare, or any
     * overprint makes its modules sample as a grey mix, so they score LOW confidence wherever they sit.
     * The decoder uses that to erase the damaged codewords , no need to know where the logo is.
     */
    // Fraction of the module over which the 4x4 subsamples spread. Stylised codes draw each dark module
    // as a small DOT (measured ~0.55 of the module across on a real Samsung wifi-share code), so samples
    // near module edges land on white and a full-module spread votes every dark module LIGHT , the whole
    // data field inverts and the code is unreadable. Packing the same 16 subsamples into the central 0.55
    // keeps 12 of 16 inside a dot that small, while for conventional square modules every sample still
    // lands inside the module (now further from edge bleed, so equal or better). Validated on the real
    // photo: full-window read 0 percent dark modules, 0.55 decoded the code end to end.
    private const val SAMPLE_WINDOW = 0.55

    private fun sampleWithTransform(lum: IntArray, w: Int, h: Int, n: Int, transform: PerspectiveTransform): Pair<Array<BooleanArray>, Array<IntArray>>? {
        val s = 4
        val side = n * s
        val rect = IntArray(side * side)
        for (row in 0 until n) {
            for (col in 0 until n) {
                for (sy in 0 until s) {
                    for (sx in 0 until s) {
                        val ox = 0.5 + ((sx + 0.5) / s - 0.5) * SAMPLE_WINDOW
                        val oy = 0.5 + ((sy + 0.5) / s - 0.5) * SAMPLE_WINDOW
                        val p = transform.map(col + ox, row + oy)
                        val xi = p.x.roundToInt(); val yi = p.y.roundToInt()
                        if (xi < 0 || xi >= w || yi < 0 || yi >= h) return null
                        rect[(row * s + sy) * side + (col * s + sx)] = lum[yi * w + xi] and 0xFF
                    }
                }
            }
        }
        val thr = otsuThreshold(rect)
        val grid = Array(n) { BooleanArray(n) }
        val conf = Array(n) { IntArray(n) }
        for (row in 0 until n) {
            for (col in 0 until n) {
                var dark = 0
                for (sy in 0 until s) {
                    for (sx in 0 until s) {
                        if (rect[(row * s + sy) * side + (col * s + sx)] < thr) dark++
                    }
                }
                grid[row][col] = dark * 2 > s * s
                // certainty = how lopsided the vote was. abs(dark*2 - s*s): 0 at a 50/50 split, s*s at unanimous.
                conf[row][col] = kotlin.math.abs(dark * 2 - s * s)
            }
        }
        return grid to conf
    }

    /** Otsu's method: the threshold maximising between-class variance of a luminance histogram. */
    private fun otsuThreshold(vals: IntArray): Int {
        val hist = IntArray(256)
        for (v in vals) hist[v]++
        val total = vals.size
        var sumAll = 0.0
        for (i in 0 until 256) sumAll += (i * hist[i]).toDouble()
        var sumB = 0.0; var wB = 0; var best = -1.0; var thr = 128
        for (t in 0 until 256) {
            wB += hist[t]
            if (wB == 0) continue
            val wF = total - wB
            if (wF == 0) break
            sumB += (t * hist[t]).toDouble()
            val mB = sumB / wB
            val mF = (sumAll - sumB) / wF
            val between = wB.toDouble() * wF.toDouble() * (mB - mF) * (mB - mF)
            if (between > best) { best = between; thr = t }
        }
        return thr
    }

    /**
     * Build the module-space to image-space perspective transform for an n-module symbol. Uses the
     * three finder centres plus, when found, the bottom-right alignment pattern as a true fourth anchor;
     * otherwise extrapolates the fourth corner. This is the single source of truth for sampling geometry,
     * so the on-screen outline and the actual module sampling use the identical homography.
     */
    private fun buildTransform(bin: BooleanArray, w: Int, h: Int, c: Corners, n: Int): PerspectiveTransform {
        val tlmX = 3.5; val tlmY = 3.5
        val trmX = n - 3.5; val trmY = 3.5
        val blmX = 3.5; val blmY = n - 3.5
        val alignModule = alignmentCentreModule(n)
        val brcExtrap = DoublePt(c.tr.x + c.bl.x - c.tl.x, c.tr.y + c.bl.y - c.tl.y)
        if (alignModule > 0) {
            val moduleSpanPx = distD(c.tl, c.tr) / (n - 7).toDouble()
            val predicted = afflinePredict(c, brcExtrap, n, alignModule + 0.5, alignModule + 0.5)
            val found = findAlignmentPattern(bin, w, h, predicted, moduleSpanPx)
            if (found != null) {
                ScanGeom.alignFound = true
                return PerspectiveTransform.quadToQuad(
                    tlmX, tlmY, trmX, trmY, alignModule + 0.5, alignModule + 0.5, blmX, blmY,
                    c.tl.x, c.tl.y, c.tr.x, c.tr.y, found.x, found.y, c.bl.x, c.bl.y,
                )
            }
        }
        ScanGeom.alignFound = false
        return PerspectiveTransform.quadToQuad(
            tlmX, tlmY, trmX, trmY, n - 3.5, n - 3.5, blmX, blmY,
            c.tl.x, c.tl.y, c.tr.x, c.tr.y, brcExtrap.x, brcExtrap.y, c.bl.x, c.bl.y,
        )
    }

    /**
     * Build the sampling transform, then refine it iteratively. The first transform (3 finders + an
     * affine-extrapolated fourth corner) is good enough to read the format region but drifts across the
     * data field on a tilted symbol. Refinement fixes that: use the CURRENT transform , not the crude
     * affine guess , to predict where the alignment pattern's centre is, find its true sub-pixel centre
     * there, and rebuild the homography with that real point pinned as the fourth correspondence. Each
     * pass predicts the alignment more accurately, so the search lands on the true centre and the fit
     * converges. Two or three passes take the data-region error from "RS cannot correct" to readable.
     */
    private fun buildTransformRefined(bin: BooleanArray, w: Int, h: Int, c: Corners, n: Int): PerspectiveTransform {
        val alignModule = alignmentCentreModule(n)
        var transform = buildTransform(bin, w, h, c, n)
        if (alignModule <= 0) return transform

        val moduleSpanPx = distD(c.tl, c.tr) / (n - 7).toDouble()
        val tlmX = 3.5; val tlmY = 3.5
        val trmX = n - 3.5; val trmY = 3.5
        val blmX = 3.5; val blmY = n - 3.5
        val am = alignModule + 0.5

        var prev: DoublePt? = null
        for (iter in 0 until 3) {
            // Predict the alignment centre with the CURRENT transform (accurate), then find it there.
            val pred = transform.map(am, am)
            val found = findAlignmentPattern(bin, w, h, DoublePt(pred.x, pred.y), moduleSpanPx) ?: break
            // Converged if the found centre barely moved from the last pass.
            val p = prev
            if (p != null && abs(p.x - found.x) < 0.5 && abs(p.y - found.y) < 0.5) {
                transform = PerspectiveTransform.quadToQuad(
                    tlmX, tlmY, trmX, trmY, am, am, blmX, blmY,
                    c.tl.x, c.tl.y, c.tr.x, c.tr.y, found.x, found.y, c.bl.x, c.bl.y,
                )
                break
            }
            prev = found
            transform = PerspectiveTransform.quadToQuad(
                tlmX, tlmY, trmX, trmY, am, am, blmX, blmY,
                c.tl.x, c.tl.y, c.tr.x, c.tr.y, found.x, found.y, c.bl.x, c.bl.y,
            )
        }
        return transform
    }

    /** Affine prediction of an image point for module coords, using tl/tr/bl and an extrapolated br. */
    private fun afflinePredict(c: Corners, brc: DoublePt, n: Int, col: Double, row: Double): DoublePt {
        val lo = 3.5; val hi = n - 3.5
        val u = (col - lo) / (hi - lo); val v = (row - lo) / (hi - lo)
        val top = DoublePt(c.tl.x + (c.tr.x - c.tl.x) * u, c.tl.y + (c.tr.y - c.tl.y) * u)
        val bot = DoublePt(c.bl.x + (brc.x - c.bl.x) * u, c.bl.y + (brc.y - c.bl.y) * u)
        return DoublePt(top.x + (bot.x - top.x) * v, top.y + (bot.y - top.y) * v)
    }

    /**
     * Find the alignment pattern (a 5x5 concentric square, centre 1:1:1 dark/light/dark) near a
     * predicted point. Unlike a blind dark-pixel centroid, this checks the centre pixel is dark and
     * surrounded by the right ring structure, then returns the sub-pixel centroid of the small central
     * dark blob. Searches a window sized to the module pitch. Null if no plausible pattern is there.
     */
    private fun findAlignmentPattern(
        bin: BooleanArray, w: Int, h: Int, predicted: DoublePt, moduleSpanPx: Double,
    ): DoublePt? {
        val win = (moduleSpanPx * 2.5).roundToInt().coerceIn(4, 50)
        val cx = predicted.x.roundToInt(); val cy = predicted.y.roundToInt()
        // Scan the window for a dark pixel whose surrounding 1-module ring is light then dark again,
        // i.e. the alignment pattern's centre. Take the candidate nearest the prediction.
        var best: DoublePt? = null
        var bestDist = Double.MAX_VALUE
        val step = 1
        var yy = cy - win
        while (yy <= cy + win) {
            var xx = cx - win
            while (xx <= cx + win) {
                if (yy in 0 until h && xx in 0 until w && dark(bin, w, xx, yy)) {
                    if (looksLikeAlignmentCentre(bin, w, h, xx, yy, moduleSpanPx)) {
                        val centre = darkBlobCentroid(bin, w, h, xx, yy, (moduleSpanPx).roundToInt().coerceIn(1, 8))
                        val d = (centre.x - predicted.x) * (centre.x - predicted.x) +
                            (centre.y - predicted.y) * (centre.y - predicted.y)
                        if (d < bestDist) { bestDist = d; best = centre }
                    }
                }
                xx += step
            }
            yy += step
        }
        return best
    }

    /** True if (x,y) sits in a dark centre ringed by light then dark, ~ the alignment 1:1:1 signature. */
    private fun looksLikeAlignmentCentre(
        bin: BooleanArray, w: Int, h: Int, x: Int, y: Int, moduleSpanPx: Double,
    ): Boolean {
        val m = moduleSpanPx.roundToInt().coerceIn(1, 12)
        // one module out should be light, two modules out should be dark (the outer ring).
        fun at(dx: Int, dy: Int): Boolean {
            val xi = x + dx; val yi = y + dy
            if (xi < 0 || xi >= w || yi < 0 || yi >= h) return false
            return dark(bin, w, xi, yi)
        }
        val lightRing = !at(m, 0) && !at(-m, 0) && !at(0, m) && !at(0, -m)
        val darkRing = at(2 * m, 0) && at(-2 * m, 0) && at(0, 2 * m) && at(0, -2 * m)
        return lightRing && darkRing
    }

    /** Centroid of the connected dark blob around (x,y), bounded to a small radius. */
    private fun darkBlobCentroid(bin: BooleanArray, w: Int, h: Int, x: Int, y: Int, radius: Int): DoublePt {
        var sx = 0L; var sy = 0L; var cnt = 0L
        for (dy in -radius..radius) {
            for (dx in -radius..radius) {
                val xi = x + dx; val yi = y + dy
                if (xi in 0 until w && yi in 0 until h && dark(bin, w, xi, yi)) { sx += xi; sy += yi; cnt++ }
            }
        }
        if (cnt == 0L) return DoublePt(x.toDouble(), y.toDouble())
        return DoublePt(sx.toDouble() / cnt, sy.toDouble() / cnt)
    }


    /**
     * Module coordinate of the bottom-right alignment pattern's centre (on the diagonal), used as the
     * fourth sampling anchor. For any version >= 2 this is n-7: the last alignment coordinate always lands
     * there, and it never collides with a finder. This used to return a value only for v2..v6, which left
     * every larger code (a v9 enrol code, a v11) sampling with no bottom-right anchor , the affine
     * parallelogram guess drifts under perspective, the far modules read wrong, and Reed-Solomon then
     * miscorrects into different garbage each frame. Anchoring on the real alignment pattern is how a
     * production decoder keeps the far corner accurate; extending it here is what makes dense codes decode.
     */
    private fun alignmentCentreModule(n: Int): Int = if (n >= 25) n - 7 else 0

    /** Estimate module size in pixels by scanning the finder horizontally at the tl centre. */
    private fun estimateModuleSize(bin: BooleanArray, w: Int, h: Int, c: Corners): Double? {
        val cx = c.tl.x.roundToInt().coerceIn(0, w - 1)
        val cy = c.tl.y.roundToInt().coerceIn(0, h - 1)
        if (!dark(bin, w, cx, cy)) return null
        // Measure the FULL finder width (7 modules: dark-light-dark[3]-light-dark) by walking out from
        // the centre through the run pattern in both directions. This is far more precise than the
        // central 3-module run alone , more pixels to divide by means a tighter module-size estimate,
        // which is what stops the version search from wobbling between two neighbours.
        // central dark run (3 modules)
        var left = cx; while (left > 0 && dark(bin, w, left - 1, cy)) left--
        var right = cx; while (right < w - 1 && dark(bin, w, right + 1, cy)) right++
        // light ring (1 module each side)
        var l2 = left - 1; while (l2 > 0 && !dark(bin, w, l2 - 1, cy)) l2--
        var r2 = right + 1; while (r2 < w - 1 && !dark(bin, w, r2 + 1, cy)) r2++
        // outer dark ring (1 module each side)
        var l3 = l2 - 1; while (l3 > 0 && dark(bin, w, l3 - 1, cy)) l3--
        var r3 = r2 + 1; while (r3 < w - 1 && dark(bin, w, r3 + 1, cy)) r3++
        val fullWidth = (r3 - l3 + 1).toDouble()
        // sanity: the full finder should be clearly wider than the central run
        val centralWidth = (right - left + 1).toDouble()
        if (centralWidth < 3.0 || fullWidth < centralWidth * 1.5) {
            // outer rings not cleanly found; fall back to the central-run estimate
            return centralWidth / 3.0
        }
        return fullWidth / 7.0
    }

    /**
     * Score a grid by how cleanly its timing patterns alternate. Row 6 and column 6 run perfectly
     * alternating dark/light from module 8 to n-8, so a correctly-sized, correctly-sampled grid has a
     * transition on ALMOST EVERY step (ideal count = n-17). A WRONG version samples at the wrong pitch
     * and shows runs of same-colour modules, so far fewer transitions. We score each line by how close
     * it is to ideal (penalising the shortfall) and take the worse of row and column, so a grid only
     * scores high when BOTH timing lines are clean , which decisively separates the true version from a
     * neighbour that merely passed a loose transition count.
     */
    private fun timingScore(grid: Array<BooleanArray>, n: Int): Int {
        if (n < 21) return 0
        val ideal = n - 17
        fun lineScore(get: (Int) -> Boolean): Int {
            var t = 0
            var prev = get(8)
            for (i in 9 until n - 8) { if (get(i) != prev) t++; prev = get(i) }
            // closeness to ideal: full marks at ideal, dropping off as transitions fall short
            return ideal - abs(ideal - t)
        }
        val rowS = lineScore { i -> grid[6][i] }
        val colS = lineScore { i -> grid[i][6] }
        return min(rowS, colS)
    }

    private fun dist(a: Pt, b: Pt): Double = hypot(a.x - b.x, a.y - b.y)
    private fun distD(a: DoublePt, b: DoublePt): Double = hypot(a.x - b.x, a.y - b.y)

    /** Predicts where a missing third finder must sit given two found ones, and searches a tight
     *  window at each hypothesis with a lenient vertical-profile check. Geometry: if a,b are two
     *  corners of the finder right-angle, the third is at a+(b-a) rotated ±90° about a or about b;
     *  if a,b are the DIAGONAL pair, the third is at the midpoint ± half-diagonal rotated 90°.
     *  Six candidate spots, each a (3·mod)-radius window , cheap, targeted, decoder-judged. */
    private fun rescueThirdFinder(bin: BooleanArray, w: Int, h: Int, a: Pt, b: Pt): Pt? {
        val mod = if (a.mod > 0 && b.mod > 0) (a.mod + b.mod) / 2.0 else maxOf(a.mod, b.mod, 3.0)
        val dx = b.x - a.x; val dy = b.y - a.y
        val spots = listOf(
            Pt(a.x - dy, a.y + dx), Pt(a.x + dy, a.y - dx),           // right angle at a
            Pt(b.x - dy, b.y + dx), Pt(b.x + dy, b.y - dx),           // right angle at b
            Pt((a.x + b.x) / 2 - dy / 2, (a.y + b.y) / 2 + dx / 2),   // a,b diagonal
            Pt((a.x + b.x) / 2 + dy / 2, (a.y + b.y) / 2 - dx / 2),
        )
        val win = (mod * 3).toInt().coerceIn(4, 40)
        for (sp in spots) {
            val cx = sp.x.toInt(); val cy = sp.y.toInt()
            if (cx < win || cy < win || cx >= w - win || cy >= h - win) continue
            // Lenient local check: walk a few rows around the spot looking for any 1:1:3:1:1-ish
            // horizontal run whose centre lands inside the window; confirm with verticalCentre.
            var y = cy - win
            while (y <= cy + win) {
                var x = (cx - win).coerceAtLeast(0)
                val xEnd = (cx + win).coerceAtMost(w - 1)
                while (x < xEnd) {
                    if (dark(bin, w, x, y)) {
                        val runs = IntArray(5)
                        var ri = 0
                        var px = x
                        var expectDark = true
                        val runStart = x
                        while (px <= xEnd && ri < 5) {
                            var run = 0
                            while (px <= xEnd && dark(bin, w, px, y) == expectDark) { run++; px++ }
                            runs[ri++] = run
                            expectDark = !expectDark
                        }
                        if (ri == 5 && matches11311(runs)) {
                            val midStart = runStart + runs[0] + runs[1]
                            val cXd = midStart + runs[2] / 2.0
                            val cYd = verticalCentre(bin, w, h, cXd.toInt(), y)
                            if (!cYd.isNaN() &&
                                kotlin.math.abs(cXd - sp.x) <= win && kotlin.math.abs(cYd - sp.y) <= win) {
                                return Pt(cXd, cYd, runs.sum() / 7.0)
                            }
                        }
                        x = px
                    } else x++
                }
                y += 2
            }
        }
        return null
    }
}
