package com.localghost.app.update

/**
 * What the mirror says about server releases, read the way the box reads it (tools/mirror_fetch.sh):
 * MANIFEST.txt is three header lines ("# LocalGhost Mirror Manifest", "# Build: <build>",
 * "# Signed: <time>"), a blank line, then "<sha256>  /<build>/<set>/<file>". The server set holds
 * the release bundle, RELEASE.txt (version, commit, date, the commits since the last tag), its
 * NOTICE.txt and TERMS-*.txt.
 *
 * The phone does not check the manifest's signature: it only decides whether to ask the person,
 * and the box checks everything, with the key it already holds, before it puts a release on. A
 * forged manifest can make the phone offer a release; it cannot make the box take one.
 *
 * Pure, so the tests read it.
 */
object ReleaseInfo {
    /** One file of the server set: its hash, its path on the mirror (/<build>/server/<name>), its name. */
    data class SetFile(val sha256: String, val path: String, val name: String)

    data class Manifest(val build: String, val server: List<SetFile>)

    /** [name]: the release's name ("wisp"), "" for one without; [go]: the Go that built it, "" when
     *  the notes do not say. */
    data class Release(val version: String, val commit: String, val date: String, val bundle: String,
                       val since: String, val changes: List<String>, val name: String = "", val go: String = "") {
        /** "wisp 0.0.1", or the version alone. */
        val label: String get() = if (name.isNotBlank()) "$name $version" else version
    }

    private val LINE = Regex("^([0-9a-f]{64})  (/([0-9]{8}T[0-9]{6}Z)/server/([A-Za-z0-9][A-Za-z0-9._+-]*))$")

    /**
     * Whether a line of the manifest is one the phone reads: the header (the title, the build, the
     * signing time, the blank after them) and the server set's files. The manifest lists every file
     * of every set, the elevation tiles among them (tens of thousands of lines, megabytes), so the
     * phone keeps only these as it reads, and stores only these.
     */
    fun keep(line: String): Boolean {
        val l = line.trim()
        return l.isEmpty() || l.startsWith("#") || l.contains("/server/")
    }

    fun manifest(text: String): Manifest? {
        val lines = text.lines()
        if (lines.firstOrNull() != "# LocalGhost Mirror Manifest") return null
        val build = lines.firstOrNull { it.startsWith("# Build: ") }?.removePrefix("# Build: ")?.trim() ?: return null
        val files = lines.mapNotNull { l -> LINE.matchEntire(l.trim())?.let { m ->
            if (m.groupValues[3] != build) null else SetFile(m.groupValues[1], m.groupValues[2], m.groupValues[4])
        } }
        return Manifest(build, files)
    }

    fun release(text: String): Release? {
        val kv = HashMap<String, String>()
        val changes = ArrayList<String>()
        var inChanges = false
        for (raw in text.lines()) {
            if (inChanges) {
                if (raw.startsWith("  ")) { raw.trim().takeIf { it.isNotEmpty() }?.let { changes.add(it) }; continue }
                inChanges = false
            }
            if (raw.trim() == "changes:") { inChanges = true; continue }
            val i = raw.indexOf('=')
            if (i > 0) kv[raw.substring(0, i).trim()] = raw.substring(i + 1).trim()
        }
        // The notes come from the web host, unsigned, and are shown in a notification and on
        // the DEPLOY prompt before the box has checked anything: each field is held to the shape
        // it has, and one that is not that shape is left out (the version: no release at all).
        val v = kv["version"]?.takeIf { VERSION.matches(it) } ?: return null
        fun field(key: String, shape: Regex): String = kv[key]?.takeIf { shape.matches(it) } ?: ""
        val lines = changes.take(MAX_CHANGES).map { c ->
            val t = c.filter { !it.isISOControl() }.trim()
            if (t.length <= MAX_CHANGE_CHARS) t else t.take(MAX_CHANGE_CHARS) + "…"
        }.filter { it.isNotEmpty() }
        return Release(v, field("commit", COMMIT), field("date", DATE), field("bundle", FILE), field("since", SINCE), lines,
            field("name", NAME), field("go", GO))
    }

    private val VERSION = Regex("^v?[0-9]{1,4}(\\.[0-9]{1,4}){1,3}$")
    private val NAME = Regex("^[a-z0-9][a-z0-9 -]{0,23}$")
    private val COMMIT = Regex("^[0-9a-f]{3,40}$")
    private val DATE = Regex("^\\d{4}-\\d{2}-\\d{2}(T\\d{2}:\\d{2}(:\\d{2})?(Z|[+-]\\d{2}:?\\d{2})?)?$")
    private val FILE = Regex("^[A-Za-z0-9][A-Za-z0-9._+-]{0,80}$")
    private val SINCE = Regex("^(the first release|[A-Za-z0-9][A-Za-z0-9._-]{0,30})$") // a tag, or what release_build.sh writes without one
    private val GO = Regex("^(go)?[0-9][0-9a-z.]{0,15}$")
    const val MAX_CHANGES = 200
    const val MAX_CHANGE_CHARS = 160

    /** A commit's first seven characters, "" for none. */
    fun short(commit: String): String = commit.trim().take(7)

    /**
     * An RFC 3339 time ("2026-10-04T17:12:00Z") as "4 Oct 2026, 17:12 UTC"; a bare date as
     * "4 Oct 2026"; anything else as it came.
     */
    fun at(iso: String): String {
        val m = Regex("^(\\d{4})-(\\d{2})-(\\d{2})(?:T(\\d{2}):(\\d{2})(?::\\d{2})?(Z|[+-]\\d{2}:?\\d{2})?)?$").matchEntire(iso.trim()) ?: return iso.trim()
        val months = listOf("Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec")
        val mon = m.groupValues[2].toInt()
        if (mon !in 1..12) return iso.trim()
        val day = "${m.groupValues[3].toInt()} ${months[mon - 1]} ${m.groupValues[1]}"
        if (m.groupValues[4].isEmpty()) return day
        val zone = when (val z = m.groupValues[6]) { "Z" -> " UTC"; "" -> ""; else -> " $z" }
        return "$day, ${m.groupValues[4]}:${m.groupValues[5]}$zone"
    }

    /**
     * One line for a build: "wisp 0.0.4 (1062b7f)", then what is known of it: "built 4 Oct 2026,
     * 17:12 UTC", "Go 1.27.1" (the runtime's "go1.27.1" shortened).
     */
    fun describe(label: String, commit: String, builtAt: String, go: String): String {
        val sb = StringBuilder(label)
        if (commit.isNotBlank()) sb.append(" (").append(short(commit)).append(')')
        val more = ArrayList<String>()
        if (builtAt.isNotBlank()) more.add("built " + at(builtAt))
        if (go.isNotBlank()) more.add("Go " + go.trim().removePrefix("go"))
        if (more.isNotEmpty()) sb.append(", ").append(more.joinToString(", "))
        return sb.toString()
    }

    /**
     * Whether [offered] is newer than what the box runs ([running]: a release version, or a build
     * from source like "v0.9.2-5-gabc1234-dirty", or "dev"). Numbers compared part by part; a
     * build from source counts as its tag plus the commits on top (so the tag itself is not newer,
     * the next one is). Unknown ("dev", "") is always older: the person decides.
     */
    fun newer(offered: String, running: String): Boolean {
        val o = numbers(offered) ?: return false
        val r = numbers(running) ?: return true
        for (i in 0 until maxOf(o.size, r.size)) {
            val a = o.getOrElse(i) { 0 }
            val b = r.getOrElse(i) { 0 }
            if (a != b) return a > b
        }
        return false
    }

    private fun numbers(v: String): List<Int>? {
        val core = v.trim().removePrefix("v").substringBefore('-').substringBefore('+')
        if (core.isEmpty()) return null
        val parts = core.split('.')
        // a bare commit hash (git describe --always, no tag yet) can be all digits: not a version
        if (parts.size == 1 && core.length >= 7) return null
        return parts.map { it.toIntOrNull() ?: return null }
    }
}
