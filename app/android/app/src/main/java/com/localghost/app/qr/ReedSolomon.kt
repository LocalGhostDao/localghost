package com.localghost.app.qr

/**
 * Reed-Solomon errors-only decoder over GF(256) for QR. Ported faithfully from the reference
 * implementation that was validated against round-trip tests (1..t errors corrected, boundary
 * positions, and beyond-capacity correctly rejected rather than mis-corrected).
 *
 * decode(block, nsym) returns the corrected block, or null if it cannot be corrected (too many
 * errors). nsym is the number of EC codewords in the block. Polynomials are high-order-first,
 * matching the reference's list conventions.
 *
 * A null return is the safe outcome: in the trust-anchor path we would rather fail to scan than
 * "correct" to wrong bytes.
 */
internal object ReedSolomon {
    private val gf = GaloisField

    fun decode(block: IntArray, nsym: Int): IntArray? {
        val synd = calcSyndromes(block, nsym)
        if (synd.max() == 0) return block          // no errors

        val errLoc = findErrorLocator(synd, nsym)
        val positions = findErrors(errLoc.reversedArray(), block.size) ?: return null
        // The same two guards the erasure path has always had. Without the capacity check a
        // locator with more roots than the code can correct was "corrected" anyway; without the
        // re-check a miscorrection was returned as clean , and "clean" is the path the scanner
        // trusts on a single frame.
        if (2 * positions.size > nsym) return null
        val corrected = correctErrata(block, synd, positions) ?: return null
        if (calcSyndromes(corrected, nsym).max() != 0) return null
        return corrected
    }

    /**
     * Erasures kept below the parity count. With e = nsym erasures and no errors the system has as
     * many unknowns as equations and EVERY input "decodes" , pure interpolation, no check left, a
     * fabricated block passed back as corrected. Keeping four parity symbols in reserve leaves
     * 32 bits of check, so a wrong guess fails with odds around 2^-32 instead of never.
     */
    const val ERASE_MARGIN = 4

    /**
     * Erasure-aware decode. When we already KNOW where the damage is , for a QR with a centre logo, the
     * modules under the logo , we pass those codeword positions as erasures. Reed-Solomon corrects up to
     * `nsym` erasures, versus only `nsym/2` unknown errors, because an erasure's location is free and
     * only its value must be solved. So a logo that is hopeless as "errors" (too many) becomes
     * recoverable as "erasures". Capacity rule enforced: 2*errors + erasures <= nsym. Returns null if it
     * cannot correct (the safe outcome on the trust-anchor path).
     */
    fun decode(block: IntArray, nsym: Int, erasures: IntArray): IntArray? {
        if (erasures.isEmpty()) return decode(block, nsym)
        if (erasures.size > nsym - ERASE_MARGIN) return null
        val msg = block.copyOf()
        for (e in erasures) if (e in msg.indices) msg[e] = 0
        val synd = calcSyndromes(msg, nsym)
        if (synd.max() == 0) return msg
        val fsynd = forneySyndromes(synd, erasures, msg.size)
        val errLoc = findErrorLocator(fsynd, nsym, erasures.size)
        val errPos = findErrors(errLoc.reversedArray(), msg.size) ?: return null
        if (2 * errPos.size + erasures.size > nsym) return null   // beyond capacity
        val all = (erasures.toList() + errPos.toList()).toIntArray()
        val corrected = correctErrata(msg, synd, all) ?: return null
        if (calcSyndromes(corrected, nsym).max() != 0) return null
        return corrected
    }

    // Forney syndromes: fold the known erasure locations out of the syndromes so plain Berlekamp-Massey
    // can then find the remaining unknown errors. synd has a leading 0 (length nsym+1); drop it.
    private fun forneySyndromes(synd: IntArray, erasures: IntArray, nmess: Int): IntArray {
        val fsynd = IntArray(synd.size - 1) { synd[it + 1] }
        for (p in erasures) {
            val x = gf.pow(2, nmess - 1 - p)
            for (j in 0 until fsynd.size - 1) fsynd[j] = gf.mul(fsynd[j], x) xor fsynd[j + 1]
        }
        return fsynd
    }

    /** Number of non-zero syndromes: a rough lower bound on how corrupt a block is, for diagnostics. */
    fun syndromeErrorCount(block: IntArray, nsym: Int): Int {
        val synd = calcSyndromes(block, nsym)
        return synd.count { it != 0 }
    }

    // synd has length nsym+1 with a leading 0, matching the reference.
    private fun calcSyndromes(msg: IntArray, nsym: Int): IntArray {
        val out = IntArray(nsym + 1)
        for (i in 0 until nsym) out[i + 1] = polyEval(msg, gf.pow(2, i))
        return out
    }

    private fun polyEval(p: IntArray, x: Int): Int {
        var y = p[0]
        for (i in 1 until p.size) y = gf.mul(y, x) xor p[i]
        return y
    }

    private fun polyScale(p: IntArray, x: Int): IntArray =
        IntArray(p.size) { gf.mul(p[it], x) }

    private fun polyAdd(p: IntArray, q: IntArray): IntArray {
        val r = IntArray(maxOf(p.size, q.size))
        for (i in p.indices) r[i + r.size - p.size] = p[i]
        for (i in q.indices) r[i + r.size - q.size] = r[i + r.size - q.size] xor q[i]
        return r
    }

    private fun polyMul(p: IntArray, q: IntArray): IntArray {
        val r = IntArray(p.size + q.size - 1)
        for (j in q.indices) for (i in p.indices) r[i + j] = r[i + j] xor gf.mul(p[i], q[j])
        return r
    }

    private fun findErrorLocator(synd: IntArray, nsym: Int, eraseCount: Int = 0): IntArray {
        var errLoc = intArrayOf(1)
        var oldLoc = intArrayOf(1)
        val syndShift = if (synd.size > nsym) synd.size - nsym else 0
        for (i in 0 until (nsym - eraseCount)) {
            val k = i + syndShift
            var delta = synd[k]
            for (j in 1 until errLoc.size) {
                delta = delta xor gf.mul(errLoc[errLoc.size - 1 - j], synd[k - j])
            }
            oldLoc = oldLoc + 0
            if (delta != 0) {
                if (oldLoc.size > errLoc.size) {
                    val newLoc = polyScale(oldLoc, delta)
                    oldLoc = polyScale(errLoc, gf.inverse(delta))
                    errLoc = newLoc
                }
                errLoc = polyAdd(errLoc, polyScale(oldLoc, delta))
            }
        }
        var start = 0
        while (start < errLoc.size && errLoc[start] == 0) start++
        return errLoc.copyOfRange(start, errLoc.size)
    }

    // err_loc passed in already reversed (low-order first), matching reference call site.
    private fun findErrors(errLocRev: IntArray, nmess: Int): IntArray? {
        val errs = errLocRev.size - 1
        val positions = ArrayList<Int>()
        for (i in 0 until nmess) {
            if (polyEval(errLocRev, gf.pow(2, i)) == 0) positions.add(nmess - 1 - i)
        }
        if (positions.size != errs) return null      // too many errors to locate
        return positions.toIntArray()
    }

    private fun findErrataLocator(ePos: IntArray): IntArray {
        var eLoc = intArrayOf(1)
        for (i in ePos) {
            eLoc = polyMul(eLoc, polyAdd(intArrayOf(1), intArrayOf(gf.pow(2, i), 0)))
        }
        return eLoc
    }

    private fun findErrorEvaluator(synd: IntArray, errLoc: IntArray, nsym: Int): IntArray {
        val remainder = polyMul(synd, errLoc)
        return remainder.copyOfRange(remainder.size - (nsym + 1), remainder.size)
    }

    private fun correctErrata(msgIn: IntArray, synd: IntArray, errPos: IntArray): IntArray? {
        val coefPos = IntArray(errPos.size) { msgIn.size - 1 - errPos[it] }
        val errLoc = findErrataLocator(coefPos)
        val errEval = findErrorEvaluator(synd.reversedArray(), errLoc, errLoc.size - 1).reversedArray()

        val x = IntArray(coefPos.size) { gf.pow(2, coefPos[it]) }
        val e = IntArray(msgIn.size)
        for (i in x.indices) {
            val xiInv = gf.inverse(x[i])
            var errLocPrime = 1
            for (j in x.indices) {
                if (j != i) errLocPrime = gf.mul(errLocPrime, 1 xor gf.mul(xiInv, x[j]))
            }
            if (errLocPrime == 0) return null
            var y = polyEval(errEval.reversedArray(), xiInv)
            y = gf.mul(gf.pow(x[i], 1), y)
            e[errPos[i]] = gf.div(y, errLocPrime)
        }
        return polyAdd(msgIn, e)
    }
}
