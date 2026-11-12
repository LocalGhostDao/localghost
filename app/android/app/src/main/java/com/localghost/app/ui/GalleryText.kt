package com.localghost.app.ui

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/** The gallery's words and numbers, pure, so the tests can read them. */
object GalleryText {
    /** A clip's running time as the cell shows it: 0:07, 1:42, 1:03:05. Blank for none. */
    fun duration(seconds: Double): String {
        if (seconds <= 0) return ""
        val total = Math.round(seconds).toInt()
        val h = total / 3600
        val m = (total % 3600) / 60
        val s = total % 60
        return if (h > 0) "%d:%02d:%02d".format(h, m, s) else "%d:%02d".format(m, s)
    }

    /** Bytes as the detail shows them: 814 KB, 3.2 MB, 1.4 GB. */
    fun size(bytes: Long): String = when {
        bytes <= 0 -> ""
        bytes < 1024L * 1024 -> "${(bytes + 512) / 1024} KB"
        bytes < 1024L * 1024 * 1024 -> "%.1f MB".format(java.util.Locale.UK, bytes / 1048576.0)
        else -> "%.1f GB".format(java.util.Locale.UK, bytes / 1073741824.0)
    }

    /** The heading of a detail: "> PHOTO", "> VIDEO · 1:42". */
    fun heading(kind: String, seconds: Double): String {
        val k = if (kind == "video") "VIDEO" else "PHOTO"
        val d = duration(seconds)
        return if (d.isEmpty()) "> $k" else "> $k · $d"
    }

    /** The line under the name: "Sat 24 Oct 2026, 00:22 · 3.2 MB". */
    fun taken(takenAt: Long, bytes: Long, zone: ZoneId = ZoneId.systemDefault()): String {
        if (takenAt <= 0) return size(bytes)
        val t = Instant.ofEpochSecond(takenAt).atZone(zone)
        val d = t.format(DateTimeFormatter.ofPattern("EEE d MMM yyyy, HH:mm", java.util.Locale.UK))
        val s = size(bytes)
        return if (s.isEmpty()) d else "$d · $s"
    }

    /** The category order the tag groups appear in: who first, then where, then what. */
    val categoryOrder = listOf("people", "place", "event", "activity", "animal", "food", "nature", "object", "vehicle", "text", "style", "other")

    /** The tags grouped and ordered for display; tags with no category land in "other"; an empty
     *  grouping (an older box) falls back to one group of all the tags. */
    fun groups(tags: List<String>, byCategory: Map<String, List<String>>): List<Pair<String, List<String>>> {
        if (tags.isEmpty()) return emptyList()
        if (byCategory.isEmpty()) return listOf("tags" to tags)
        val placed = byCategory.values.flatten().toSet()
        val rest = tags.filter { it !in placed }
        val all = byCategory.toMutableMap()
        if (rest.isNotEmpty()) all["other"] = (all["other"] ?: emptyList()) + rest
        val known = categoryOrder.filter { all[it]?.isNotEmpty() == true }.map { it to all.getValue(it) }
        val unknown = all.keys.filter { it !in categoryOrder }.sorted().map { it to all.getValue(it) }
        return known + unknown
    }

    /** The grouping after an edit on the phone: removed tags leave their group, added ones wait
     *  in "other" until the box's categoriser sees them. */
    fun regroup(byCategory: Map<String, List<String>>, tags: List<String>): Map<String, List<String>> {
        val kept = byCategory.mapValues { (_, v) -> v.filter { it in tags } }.filterValues { it.isNotEmpty() }
        val placed = kept.values.flatten().toSet()
        val rest = tags.filter { it !in placed }
        if (rest.isEmpty()) return kept
        return kept + ("other" to ((kept["other"] ?: emptyList()) + rest))
    }

    /** What the hint under the picture says. */
    fun hint(kind: String): String =
        if (kind == "video") "tap to play , streamed from your box, nothing kept on the phone"
        else "tap to open full screen · pinch to zoom"
}
