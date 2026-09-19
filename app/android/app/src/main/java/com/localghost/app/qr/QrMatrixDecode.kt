package com.localghost.app.qr

/**
 * Decodes a QR module matrix (already sampled to a square boolean grid, true = dark) back to its
 * byte-mode text. This is the inverse of the box's from-scratch encoder, restricted to the same
 * subset: byte mode, versions 1-10, EC level M, which is all the enrolment QR ever uses. It reuses
 * the Reed-Solomon error correction already in this package.
 *
 * It does NOT do image processing: turning a camera frame into this boolean grid (finder detection,
 * sampling) is the QrScanner's job. Keeping the matrix decode separate makes it testable against the
 * encoder without a camera.
 */
object QrMatrixDecode {

    class DecodeError(msg: String) : Exception(msg)

    // Diagnostic: how the last successful decode was reached , "clean" (errors-only), "conf" (erase
    // low-confidence) or "logo" (adaptive centre-box erasure). Erasure-based reads on a code with no logo
    // are the tell-tale of a badly-sampled grid manufacturing a valid-but-wrong payload, so surfacing this
    // distinguishes a real decode from that; the scanner also uses it to decide whether a single frame is
    // enough to accept an enrol link ("clean") or a second confirming frame is required. Reset each decode().
    @Volatile var lastPath: String = ""

    // Debug logging gate. false = only successful decodes are logged (quiet during a failing scan).
    // Set true to log every format read and per-block RS failure (the first8 byte dump) for diagnosis.
    // Remove with the rest of the LGScan diagnostics before release.
    private const val VERBOSE = false

    // Block parameters per EC level and version: ecPerBlock, g1blocks, g1dataCW, g2blocks, g2dataCW.
    // (ISO/IEC 18004 Table 9.) Online generators default to level L; our own encoder uses M. The level
    // is read from the format bits at decode time, so any standard QR decodes, not just our own.
    private val blockTable = mapOf(
        'L' to mapOf(
            1 to intArrayOf(7, 1, 19, 0, 0), 2 to intArrayOf(10, 1, 34, 0, 0), 3 to intArrayOf(15, 1, 55, 0, 0),
            4 to intArrayOf(20, 1, 80, 0, 0), 5 to intArrayOf(26, 1, 108, 0, 0), 6 to intArrayOf(18, 2, 68, 0, 0),
            7 to intArrayOf(20, 2, 78, 0, 0), 8 to intArrayOf(24, 2, 97, 0, 0), 9 to intArrayOf(30, 2, 116, 0, 0),
            10 to intArrayOf(18, 2, 68, 2, 69),
            11 to intArrayOf(20, 4, 81, 0, 0), 12 to intArrayOf(24, 2, 92, 2, 93), 13 to intArrayOf(26, 4, 107, 0, 0),
            14 to intArrayOf(30, 3, 115, 1, 116), 15 to intArrayOf(22, 5, 87, 1, 88), 16 to intArrayOf(24, 5, 98, 1, 99),
            17 to intArrayOf(28, 1, 107, 5, 108), 18 to intArrayOf(30, 5, 120, 1, 121), 19 to intArrayOf(28, 3, 113, 4, 114),
            20 to intArrayOf(28, 3, 107, 5, 108),
        ),
        'M' to mapOf(
            1 to intArrayOf(10, 1, 16, 0, 0), 2 to intArrayOf(16, 1, 28, 0, 0), 3 to intArrayOf(26, 1, 44, 0, 0),
            4 to intArrayOf(18, 2, 32, 0, 0), 5 to intArrayOf(24, 2, 43, 0, 0), 6 to intArrayOf(16, 4, 27, 0, 0),
            7 to intArrayOf(18, 4, 31, 0, 0), 8 to intArrayOf(22, 2, 38, 2, 39), 9 to intArrayOf(22, 3, 36, 2, 37),
            10 to intArrayOf(26, 4, 43, 1, 44),
            11 to intArrayOf(30, 1, 50, 4, 51), 12 to intArrayOf(22, 6, 36, 2, 37), 13 to intArrayOf(22, 8, 37, 1, 38),
            14 to intArrayOf(24, 4, 40, 5, 41), 15 to intArrayOf(24, 5, 41, 5, 42), 16 to intArrayOf(28, 7, 45, 3, 46),
            17 to intArrayOf(28, 10, 46, 1, 47), 18 to intArrayOf(26, 9, 43, 4, 44), 19 to intArrayOf(26, 3, 44, 11, 45),
            20 to intArrayOf(26, 3, 41, 13, 42),
        ),
        'Q' to mapOf(
            1 to intArrayOf(13, 1, 13, 0, 0), 2 to intArrayOf(22, 1, 22, 0, 0), 3 to intArrayOf(18, 2, 17, 0, 0),
            4 to intArrayOf(26, 2, 24, 0, 0), 5 to intArrayOf(18, 2, 15, 2, 16), 6 to intArrayOf(24, 4, 19, 0, 0),
            7 to intArrayOf(18, 2, 14, 4, 15), 8 to intArrayOf(22, 4, 18, 2, 19), 9 to intArrayOf(20, 4, 16, 4, 17),
            10 to intArrayOf(24, 6, 19, 2, 20),
            11 to intArrayOf(28, 4, 22, 4, 23), 12 to intArrayOf(26, 4, 20, 6, 21), 13 to intArrayOf(24, 8, 20, 4, 21),
            14 to intArrayOf(20, 11, 16, 5, 17), 15 to intArrayOf(30, 5, 24, 7, 25), 16 to intArrayOf(24, 15, 19, 2, 20),
            17 to intArrayOf(28, 1, 22, 15, 23), 18 to intArrayOf(28, 17, 22, 1, 23), 19 to intArrayOf(26, 17, 21, 4, 22),
            20 to intArrayOf(30, 15, 24, 5, 25),
        ),
        'H' to mapOf(
            1 to intArrayOf(17, 1, 9, 0, 0), 2 to intArrayOf(28, 1, 16, 0, 0), 3 to intArrayOf(22, 2, 13, 0, 0),
            4 to intArrayOf(16, 4, 9, 0, 0), 5 to intArrayOf(22, 2, 11, 2, 12), 6 to intArrayOf(28, 4, 15, 0, 0),
            7 to intArrayOf(26, 4, 13, 1, 14), 8 to intArrayOf(26, 4, 14, 2, 15), 9 to intArrayOf(24, 4, 12, 4, 13),
            10 to intArrayOf(28, 6, 15, 2, 16),
            11 to intArrayOf(24, 3, 12, 8, 13), 12 to intArrayOf(28, 7, 14, 4, 15), 13 to intArrayOf(22, 12, 11, 4, 12),
            14 to intArrayOf(24, 11, 12, 5, 13), 15 to intArrayOf(24, 11, 12, 7, 13), 16 to intArrayOf(30, 3, 15, 13, 16),
            17 to intArrayOf(28, 2, 14, 17, 15), 18 to intArrayOf(28, 2, 14, 19, 15), 19 to intArrayOf(26, 9, 13, 16, 14),
            20 to intArrayOf(28, 15, 15, 10, 16),
        ),
    )

    // Alignment-pattern centre coordinates for a version, from the spec's placement rule (validated
    // against real encoder output for v2..v40, including the v32 special case). The pattern sits at every
    // (row, col) pair drawn from these coordinates, except where it would collide with a finder. Computing
    // this rather than tabulating it keeps every version correct with no 160-row table to mistype.
    private fun alignCenters(v: Int): IntArray {
        if (v == 1) return intArrayOf()
        val count = v / 7 + 2
        val step = if (v == 32) 26 else (v * 4 + count * 2 + 1) / (2 * count - 2) * 2
        val last = 17 + 4 * v - 7
        val pos = IntArray(count)
        pos[0] = 6
        for (j in 1 until count) pos[j] = last - (count - 1 - j) * step
        return pos
    }

    private val maskFns = arrayOf<(Int, Int) -> Boolean>(
        { r, c -> (r + c) % 2 == 0 },
        { r, _ -> r % 2 == 0 },
        { _, c -> c % 3 == 0 },
        { r, c -> (r + c) % 3 == 0 },
        { r, c -> (r / 2 + c / 3) % 2 == 0 },
        { r, c -> (r * c) % 2 + (r * c) % 3 == 0 },
        { r, c -> ((r * c) % 2 + (r * c) % 3) % 2 == 0 },
        { r, c -> ((r + c) % 2 + (r * c) % 3) % 2 == 0 },
    )

    /** Decode a square module grid to text. Throws DecodeError on any inconsistency. */
    fun decode(grid: Array<BooleanArray>): String = decode(grid, emptyArray())

    /**
     * Decode, optionally with a per-module confidence grid (s*s = certain, 0 = a 50/50 grey, i.e. a
     * module sitting under a logo, glare, or damage). When present it lets the decoder erase the damaged
     * codewords wherever they are, so a code with a logo (centre or off-centre) still reads.
     */
    fun decode(grid: Array<BooleanArray>, conf: Array<IntArray>): String {
        val n = grid.size
        if (n < 21 || n > 97 || (n - 17) % 4 != 0) throw DecodeError("not a valid module count: $n")
        for (row in grid) if (row.size != n) throw DecodeError("jagged grid")
        val v = (n - 17) / 4
        if (v !in 1..20) throw DecodeError("unsupported version $v")
        val haveConf = conf.size == n

        // The sampler assigns top-left/top-right/bottom-left from finder geometry, but that assignment
        // can come out MIRRORED (a left-handed corner labelling): the finders are symmetric, the timing
        // alternates either way, so a reflected grid looks perfectly valid yet reads every data and
        // format module from the mirror-image cell. Rotating a mirrored grid only gives more mirrored
        // grids , no rotation un-reflects it , so RS fails in all four. Resolve it by trying the full
        // dihedral group: the grid and its transpose (the mirror), each in four 90-degree rotations,
        // eight orientations in total. The confidence grid is rotated in lock-step so erasures stay
        // aligned to their modules. The correct one is whichever actually format-reads and corrects.
        var lastErr: Exception? = null
        for (mirror in 0 until 2) {
            var g = if (mirror == 1) transpose(grid, n) else grid
            var cf = if (haveConf) (if (mirror == 1) transposeI(conf, n) else conf) else emptyArray()
            for (rot in 0 until 4) {
                try {
                    val out = decodeOriented(g, cf, n, v)
                    if (VERBOSE) android.util.Log.d("LGScan", "decoded at mirror=$mirror rotation=${rot * 90}deg: ${out.length} chars")
                    return out
                } catch (e: Exception) {
                    lastErr = e
                    g = rotate90(g, n)
                    if (haveConf) cf = rotate90I(cf, n)
                }
            }
        }
        throw lastErr ?: DecodeError("decode failed in all orientations")
    }

    /** Transpose a square grid (reflection across the main diagonal): the mirror operation. */
    private fun transpose(grid: Array<BooleanArray>, n: Int): Array<BooleanArray> =
        Array(n) { r -> BooleanArray(n) { c -> grid[c][r] } }

    private fun transposeI(g: Array<IntArray>, n: Int): Array<IntArray> =
        Array(n) { r -> IntArray(n) { c -> g[c][r] } }

    /** Rotate a square grid 90 degrees clockwise. */
    private fun rotate90(grid: Array<BooleanArray>, n: Int): Array<BooleanArray> =
        Array(n) { r -> BooleanArray(n) { c -> grid[n - 1 - c][r] } }

    private fun rotate90I(g: Array<IntArray>, n: Int): Array<IntArray> =
        Array(n) { r -> IntArray(n) { c -> g[n - 1 - c][r] } }

    private fun decodeOriented(grid: Array<BooleanArray>, conf: Array<IntArray>, n: Int, v: Int): String {
        val (level, mask) = readFormatInfo(grid, n)
        if (VERBOSE) android.util.Log.d("LGScan", "format: level=$level mask=$mask")
        val reserved = reservedMatrix(v, n)
        val (codewords, cwConf, cwCentralDist) = readCodewords(grid, conf, reserved, n, mask)
        val data = correctAndAssemble(codewords, cwConf, cwCentralDist, v, level)
        return parseSegments(data, v)
    }

    // The 32 valid 15-bit format strings (BCH(15,5) + 0x5412 mask). The decoder reads the format bits
    // and snaps to the closest of these by Hamming distance, which corrects up to 3 misread modules.
    // Without this, a single misread format module gives a wrong EC level and mask and scrambles the
    // whole decode , the cause of the same QR reading as level Q/H/L/M frame to frame.
    private val formatCodes = intArrayOf(
        0x5412, 0x5125, 0x5E7C, 0x5B4B, 0x45F9, 0x40CE, 0x4F97, 0x4AA0,
        0x77C4, 0x72F3, 0x7DAA, 0x789D, 0x662F, 0x6318, 0x6C41, 0x6976,
        0x1689, 0x13BE, 0x1CE7, 0x19D0, 0x0762, 0x0255, 0x0D0C, 0x083B,
        0x355F, 0x3068, 0x3F31, 0x3A06, 0x24B4, 0x2183, 0x2EDA, 0x2BED,
    )

    /**
     * Reads the format info with BCH error correction. Two copies of the 15-bit format string are
     * stored; we read both, and for every candidate snap it to the nearest valid format codeword by
     * Hamming distance. The best match across both copies wins. Throws if even the closest valid code
     * is more than 3 bits away (the BCH limit), meaning the format area is too corrupted to trust.
     */
    private fun readFormatInfo(grid: Array<BooleanArray>, n: Int): Pair<Char, Int> {
        // copy 1: row 8 (cols 0..5,7,8) then col 8 (rows 7,5..0) , the standard order.
        val copy1 = arrayOf(
            intArrayOf(8, 0), intArrayOf(8, 1), intArrayOf(8, 2), intArrayOf(8, 3), intArrayOf(8, 4),
            intArrayOf(8, 5), intArrayOf(8, 7), intArrayOf(8, 8), intArrayOf(7, 8), intArrayOf(5, 8),
            intArrayOf(4, 8), intArrayOf(3, 8), intArrayOf(2, 8), intArrayOf(1, 8), intArrayOf(0, 8),
        )
        // copy 2: col 8 bottom (rows n-1..n-7) then row 8 right (cols n-8..n-1).
        val copy2 = arrayOf(
            intArrayOf(n - 1, 8), intArrayOf(n - 2, 8), intArrayOf(n - 3, 8), intArrayOf(n - 4, 8),
            intArrayOf(n - 5, 8), intArrayOf(n - 6, 8), intArrayOf(n - 7, 8), intArrayOf(8, n - 8),
            intArrayOf(8, n - 7), intArrayOf(8, n - 6), intArrayOf(8, n - 5), intArrayOf(8, n - 4),
            intArrayOf(8, n - 3), intArrayOf(8, n - 2), intArrayOf(8, n - 1),
        )
        fun read(coords: Array<IntArray>): Int {
            var bits = 0
            for (rc in coords) bits = (bits shl 1) or if (grid[rc[0]][rc[1]]) 1 else 0
            return bits
        }
        var bestDist = 99
        var bestData5 = -1
        for (raw in intArrayOf(read(copy1), read(copy2))) {
            for (d in 0 until 32) {
                val dist = Integer.bitCount(raw xor formatCodes[d])
                if (dist < bestDist) { bestDist = dist; bestData5 = d }
            }
        }
        if (bestDist > 3) {
            if (VERBOSE) android.util.Log.d("LGScan", "format too corrupted dist=$bestDist (n=$n)")
            throw DecodeError("format info too corrupted (dist $bestDist)")
        }
        if (VERBOSE) android.util.Log.d("LGScan", "format read ok dist=$bestDist data5=$bestData5")
        val level = when ((bestData5 shr 3) and 0x03) {
            0b00 -> 'M'; 0b01 -> 'L'; 0b10 -> 'H'; else -> 'Q'
        }
        val mask = bestData5 and 0x07
        return level to mask
    }

    private fun reservedMatrix(v: Int, n: Int): Array<BooleanArray> {
        val res = Array(n) { BooleanArray(n) }
        fun reserveFinder(r: Int, c: Int) {
            for (dr in -1..7) for (dc in -1..7) {
                val rr = r + dr; val cc = c + dc
                if (rr in 0 until n && cc in 0 until n) res[rr][cc] = true
            }
        }
        reserveFinder(0, 0); reserveFinder(0, n - 7); reserveFinder(n - 7, 0)
        for (i in 8 until n - 8) { res[6][i] = true; res[i][6] = true }
        res[4 * v + 9][8] = true
        val centers = alignCenters(v)
        for (r in centers) for (c in centers) {
            if ((r <= 8 && c <= 8) || (r <= 8 && c >= n - 9) || (r >= n - 9 && c <= 8)) continue
            for (dr in -2..2) for (dc in -2..2) res[r + dr][c + dc] = true
        }
        for (i in 0..8) { res[8][i] = true; res[i][8] = true }
        for (i in 0 until 8) { res[8][n - 1 - i] = true; res[n - 1 - i][8] = true }
        // Version information (v >= 7): two 18-module blocks carrying the BCH-coded version, one just left
        // of the top-right finder (rows 0..5, cols n-11..n-9) and one just above the bottom-left finder
        // (rows n-11..n-9, cols 0..5). These are function modules, not data. Failing to reserve them made
        // the reader treat 36 modules as data and corrupted every decode from v7 up , which is exactly the
        // range an info-heavy code (a full URL plus a certificate fingerprint) lands in.
        if (v >= 7) {
            for (r in 0..5) for (c in (n - 11)..(n - 9)) res[r][c] = true
            for (r in (n - 11)..(n - 9)) for (c in 0..5) res[r][c] = true
        }
        return res
    }

    private fun readCodewords(grid: Array<BooleanArray>, conf: Array<IntArray>, res: Array<BooleanArray>, n: Int, mask: Int): Triple<IntArray, IntArray, DoubleArray> {
        val haveConf = conf.size == n
        val bits = ArrayList<Int>()
        val bitConf = ArrayList<Int>()
        val bitCentralDist = ArrayList<Double>()
        var col = n - 1
        var upward = true
        while (col > 0) {
            if (col == 6) col--
            for (i in 0 until n) {
                val r = if (upward) n - 1 - i else i
                for (c in intArrayOf(col, col - 1)) {
                    if (!res[r][c]) {
                        var bit = if (grid[r][c]) 1 else 0
                        if (maskFns[mask](r, c)) bit = bit xor 1
                        bits.add(bit)
                        bitConf.add(if (haveConf) conf[r][c] else 16)
                        bitCentralDist.add(centralDist(r, c, n))
                    }
                }
            }
            col -= 2
            upward = !upward
        }
        val cws = ArrayList<Int>()
        // Per codeword: the MINIMUM module confidence among its 8 bits. A codeword is only as trustworthy
        // as its weakest module, so one logo-grey module taints the whole codeword , which is exactly the
        // codeword we want to be able to erase. Separately carry how central it is (the smallest distance-
        // to-centre over its modules), so a SOLID centre logo (which reads confident-but-wrong, invisible
        // to the confidence signal) can be erased by growing a box around the centre on the fallback path.
        val cwConf = ArrayList<Int>()
        val cwCentralDist = ArrayList<Double>()
        var i = 0
        while (i + 8 <= bits.size) {
            var v = 0
            var lo = Int.MAX_VALUE
            var minDist = Double.MAX_VALUE
            for (j in 0 until 8) {
                v = (v shl 1) or bits[i + j]
                if (bitConf[i + j] < lo) lo = bitConf[i + j]
                if (bitCentralDist[i + j] < minDist) minDist = bitCentralDist[i + j]
            }
            cws.add(v); cwConf.add(lo); cwCentralDist.add(minDist)
            i += 8
        }
        return Triple(cws.toIntArray(), cwConf.toIntArray(), cwCentralDist.toDoubleArray())
    }

    // Chebyshev distance of a module from the symbol centre, normalised by symbol size (0 at the centre,
    // ~0.5 at an edge). Carried per codeword (min over its modules) so the fallback can erase a centre
    // logo of UNKNOWN size by growing a box around the centre. Only used on the fallback path.
    private fun centralDist(r: Int, c: Int, n: Int): Double {
        val mid = (n - 1) / 2.0
        return kotlin.math.max(kotlin.math.abs(r - mid), kotlin.math.abs(c - mid)) / n
    }

    // Growing box radii (fraction of the symbol) for the adaptive centre-logo erasure, ascending so the
    // smallest box that decodes wins, leaving the most error budget for sampling noise elsewhere. A real
    // logo/label can be ~0.30 of the symbol wide , far wider than the old single fixed 0.16 box reached.
    private val ERASE_RADII = doubleArrayOf(0.16, 0.24, 0.30, 0.36)

    private fun correctAndAssemble(cws: IntArray, cwConf: IntArray, cwCentralDist: DoubleArray, v: Int, level: Char): IntArray {
        val cfg = blockTable[level]!![v]!!
        val ecpb = cfg[0]; val g1b = cfg[1]; val g1d = cfg[2]; val g2b = cfg[3]; val g2d = cfg[4]
        val sizes = ArrayList<Int>()
        repeat(g1b) { sizes.add(g1d) }
        repeat(g2b) { sizes.add(g2d) }
        val numBlocks = sizes.size
        // de-interleave data codewords (carry confidence AND centrality through the same shuffle)
        val dataBlocks = Array(numBlocks) { ArrayList<Int>() }
        val dataConf = Array(numBlocks) { ArrayList<Int>() }
        val dataDist = Array(numBlocks) { ArrayList<Double>() }
        var idx = 0
        val maxd = sizes.max()
        for (k in 0 until maxd) {
            for (bi in 0 until numBlocks) if (k < sizes[bi]) {
                dataBlocks[bi].add(cws[idx]); dataConf[bi].add(cwConf[idx]); dataDist[bi].add(cwCentralDist[idx]); idx++
            }
        }
        // de-interleave ec codewords
        val ecBlocks = Array(numBlocks) { ArrayList<Int>() }
        val ecConf = Array(numBlocks) { ArrayList<Int>() }
        val ecDist = Array(numBlocks) { ArrayList<Double>() }
        for (k in 0 until ecpb) {
            for (bi in 0 until numBlocks) {
                ecBlocks[bi].add(cws[idx]); ecConf[bi].add(cwConf[idx]); ecDist[bi].add(cwCentralDist[idx]); idx++
            }
        }
        // RS-correct each block (data + ec) with escalating strategies, each tried only if the previous
        // failed, so a clean code pays for none of them:
        //   1. errors-only        , handles a logo already within the error budget (the common case)
        //   2. erase low-confidence, handles soft/grey logos and glare, wherever they sit
        //   3. adaptive centre box , handles a SOLID centre logo/label (light or dark) that reads
        //                            confident-but-wrong: grow a box around the centre until it decodes
        lastPath = "clean"
        val out = ArrayList<Int>()
        for (bi in 0 until numBlocks) {
            val full = (dataBlocks[bi] + ecBlocks[bi]).toIntArray()
            var corrected = ReedSolomon.decode(full, ecpb)
            if (corrected == null) {
                val confFull = (dataConf[bi] + ecConf[bi])
                val ambiguous = confFull.indices.filter { confFull[it] < CONF_ERASE_BELOW }
                    .sortedBy { confFull[it] }
                if (ambiguous.isNotEmpty()) {
                    corrected = ReedSolomon.decode(full, ecpb, ambiguous.take(ecpb - ReedSolomon.ERASE_MARGIN).toIntArray())
                    if (corrected != null) lastPath = "conf"
                    if (VERBOSE) android.util.Log.d("LGScan", "conf-erasure block=$bi n=${ambiguous.size} -> ${if (corrected != null) "OK" else "fail"}")
                }
            }
            if (corrected == null) {
                // Adaptive centre-logo erasure: the logo size is unknown, so grow the box. At each radius
                // erase the codewords inside it AND any that read low-confidence , a screen photo's moire
                // fuzzes the logo's hard edges and scatters weak modules the solid box alone would miss ,
                // MOST-CENTRAL first, capped at the block's erasure budget. The smallest box that decodes
                // wins, leaving the most budget for the rest. Combining both signals recovers codes where
                // neither the box nor the confidence set manages on its own.
                val distFull = (dataDist[bi] + ecDist[bi])
                val confFull = (dataConf[bi] + ecConf[bi])
                for (radius in ERASE_RADII) {
                    // Priority order matters when the union exceeds the erasure budget: codewords INSIDE
                    // the box first (most-central first, they are the logo body and the whole point),
                    // then peripheral low-confidence ones (weakest first). Sorting by centrality alone
                    // let many scattered weak codewords evict the logo body from the list , the exact
                    // opposite of the strategy's purpose.
                    val erase = distFull.indices
                        .filter { distFull[it] < radius || confFull[it] < CONF_ERASE_BELOW }
                        .sortedWith(compareBy(
                            { if (distFull[it] < radius) 0 else 1 },
                            { if (distFull[it] < radius) distFull[it] else confFull[it].toDouble() },
                        ))
                        .take(ecpb - ReedSolomon.ERASE_MARGIN)
                    if (erase.isEmpty()) continue
                    corrected = ReedSolomon.decode(full, ecpb, erase.toIntArray())
                    if (corrected != null) { lastPath = "logo"; break }
                }
                if (VERBOSE) android.util.Log.d("LGScan", "adaptive-erasure block=$bi -> ${if (corrected != null) "OK ($lastPath)" else "fail"}")
            }
            if (corrected == null) {
                if (VERBOSE) {
                    val synErrs = ReedSolomon.syndromeErrorCount(full, ecpb)
                    android.util.Log.d("LGScan", "RS FAILED v=$v level=$level block=$bi cw=${full.size} ec=$ecpb synErrs=$synErrs")
                }
                throw DecodeError("reed-solomon failed on block $bi")
            }
            for (j in 0 until dataBlocks[bi].size) out.add(corrected[j])
        }
        if (VERBOSE) android.util.Log.d("LGScan", "RS OK v=$v dataCW=${out.size}")
        return out.toIntArray()
    }

    // A codeword whose weakest module scored below this (out of 16) is treated as ambiguous and is an
    // erasure candidate. 16 = unanimous, 0 = a 50/50 grey. Logo/glare modules land low; clean ones high.
    private const val CONF_ERASE_BELOW = 8

    private fun parseSegments(data: IntArray, v: Int): String {
        val bits = ArrayList<Int>(data.size * 8)
        for (cw in data) for (i in 7 downTo 0) bits.add((cw shr i) and 1)
        var pos = 0
        // take reads k bits, but NEVER past the end. A malformed QR can claim a length or mode that
        // runs off the data; without this guard take() would throw IndexOutOfBounds on adversarial
        // input. We treat "not enough bits" as a decode error, not a crash.
        fun take(k: Int): Int {
            if (pos + k > bits.size) throw DecodeError("ran out of bits")
            var x = 0
            repeat(k) { x = (x shl 1) or bits[pos]; pos++ }
            return x
        }
        // Segment loop. Real-world codes are not always a single byte segment: Samsung's wifi-share
        // codes (and other UTF-8 emitters) prefix an ECI segment naming the charset, and the spec allows
        // several data segments back to back before the terminator. Reading only one bare byte segment
        // made those codes fail AFTER a perfect Reed-Solomon decode, which is the worst place to fail.
        val out = java.io.ByteArrayOutputStream()
        var eci = -1
        while (pos + 4 <= bits.size) {
            val mode = take(4)
            if (mode == 0b0000) break // terminator
            when (mode) {
                0b0111 -> {
                    // ECI designator, variable length by leading bits (spec: 1, 2 or 3 bytes)
                    val first = take(8)
                    eci = when {
                        first and 0x80 == 0 -> first
                        first and 0xC0 == 0x80 -> ((first and 0x3F) shl 8) or take(8)
                        first and 0xE0 == 0xC0 -> ((first and 0x1F) shl 16) or (take(8) shl 8) or take(8)
                        else -> throw DecodeError("bad ECI header")
                    }
                }
                0b0100 -> {
                    val ccbits = if (v < 10) 8 else 16
                    val len = take(ccbits)
                    // Cap len to what the data could actually hold. A poisoned QR can put a huge length
                    // here; the read loop must be bounded by the real remaining bytes, or we either
                    // over-read (caught above) or, with a very large len, risk a big allocation. The
                    // remaining whole bytes is a hard ceiling.
                    val maxBytes = (bits.size - pos) / 8
                    if (len > maxBytes) throw DecodeError("length $len exceeds data ($maxBytes)")
                    repeat(len) { out.write(take(8)) }
                }
                else -> {
                    // Numeric/alphanumeric/kanji stay unsupported (enrol links and wifi strings are byte
                    // mode). Before any data was read that is a hard error; after a successful segment,
                    // stray non-zero padding from a lenient encoder must not void a good payload.
                    if (out.size() > 0) break else throw DecodeError("unsupported mode $mode")
                }
            }
        }
        if (out.size() == 0) throw DecodeError("no byte segment")
        val bytes = out.toByteArray()
        // ECI 26 is UTF-8; anything else (or no ECI) keeps the QR default of ISO-8859-1. Pure-ASCII
        // payloads like enrol links read identically either way.
        return if (eci == 26) String(bytes, Charsets.UTF_8) else String(bytes, Charsets.ISO_8859_1)
    }
}
