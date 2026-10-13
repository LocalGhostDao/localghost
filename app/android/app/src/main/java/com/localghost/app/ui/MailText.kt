package com.localghost.app.ui

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * MAIL's words, pure: the lines the list and the page show, the verdicts as a reader wants them,
 * who a reply goes to, the time words. No Android in here, so the tests read it.
 */
object MailText {
    /** One message as the box lists it (BoxClient fills it from the JSON). */
    data class Row(val id: Long, val direction: String, val state: String, val from: String, val fromName: String,
                   val to: List<String>, val cc: List<String>, val replyTo: String, val subject: String,
                   val sentAt: Long, val receivedAt: Long, val preview: String, val attachments: Int, val spam: String,
                   val threadKey: String = "", val messageId: String = "", val lastErr: String = "")

    /** What the box concluded; "" for a verdict it did not reach. */
    data class Verdicts(val spf: String, val dkimPassBy: String, val dkimResult: String, val dmarc: String, val dmarcPolicy: String,
                        val dmarcAligned: String, val reverse: String, val heloMatch: Boolean, val screen: List<String>,
                        val spam: String, val spamReasons: List<String>)

    data class Attachment(val id: Long, val name: String, val mime: String, val bytes: Long, val kept: Boolean)

    val folders = listOf("inbox" to "INBOX", "sent" to "SENT", "archive" to "ARCHIVE")

    /** The sender as the list names them: the name when there is one, else the address. */
    fun who(r: Row): String = when {
        r.direction == "out" -> "to " + r.to.joinToString(", ").ifEmpty { "nobody" }
        r.fromName.isNotBlank() -> r.fromName
        r.from.isNotBlank() -> r.from
        else -> "an unreadable sender"
    }

    fun subject(s: String): String = s.ifBlank { "(no subject)" }

    /** The list's second line: the preview, or what happened to a sent one. */
    fun second(r: Row): String {
        if (r.direction == "out") {
            return when (r.state) {
                "queued" -> "waiting at the relay"
                "sent" -> "delivered"
                "bounced" -> "not delivered , " + r.lastErr.ifBlank { "the receiver refused it" }
                "failed" -> "not sent , " + r.lastErr.ifBlank { "the relay refused it" }
                else -> r.state
            }
        }
        return r.preview.ifBlank { "(no text)" }
    }

    /** The small marks after the subject: unread, attachments, spam. */
    fun marks(r: Row): String {
        val m = ArrayList<String>()
        if (r.direction == "in" && r.state == "new") m.add("new")
        if (r.attachments > 0) m.add(if (r.attachments == 1) "1 file" else "${r.attachments} files")
        if (r.spam == "high") m.add("spam?")
        return m.joinToString(" · ")
    }

    /** A time the way the list wants it: the hour today, the weekday this week, the date otherwise. */
    fun whenShort(t: Long, nowS: Long, zone: ZoneId = ZoneId.systemDefault()): String {
        if (t <= 0) return ""
        val d = Instant.ofEpochSecond(t).atZone(zone)
        val now = Instant.ofEpochSecond(nowS).atZone(zone)
        return when {
            d.toLocalDate() == now.toLocalDate() -> d.format(DateTimeFormatter.ofPattern("HH:mm"))
            nowS - t < 6 * 86400 -> d.format(DateTimeFormatter.ofPattern("EEE HH:mm"))
            d.year == now.year -> d.format(DateTimeFormatter.ofPattern("d MMM"))
            else -> d.format(DateTimeFormatter.ofPattern("d MMM yyyy"))
        }
    }

    fun whenLong(t: Long, zone: ZoneId = ZoneId.systemDefault()): String =
        if (t <= 0) "" else Instant.ofEpochSecond(t).atZone(zone).format(DateTimeFormatter.ofPattern("EEEE d MMMM yyyy, HH:mm"))

    /** The verdicts as lines a reader can act on. */
    fun verdictLines(v: Verdicts): List<String> {
        val out = ArrayList<String>()
        out.add(when (v.spf) {
            "pass" -> "SPF pass: the sending server may send for the sender's domain"
            "fail" -> "SPF fail: the sender's domain says this server may not send for it"
            "softfail" -> "SPF softfail: the sender's domain doubts this server"
            "none" -> "SPF: the sender's domain publishes no record"
            "" -> "SPF not checked"
            else -> "SPF " + v.spf
        })
        out.add(when {
            v.dkimPassBy.isNotEmpty() -> "DKIM pass: signed by " + v.dkimPassBy + ", unchanged since"
            v.dkimResult == "fail" -> "DKIM fail: the signature does not match the message as it arrived"
            v.dkimResult == "" || v.dkimResult == "none" -> "DKIM: no signature"
            else -> "DKIM " + v.dkimResult
        })
        out.add(when (v.dmarc) {
            "pass" -> "DMARC: aligned with the From address (" + v.dmarcAligned + ")"
            "fail" -> "DMARC: NOT aligned with the From address" + (if (v.dmarcPolicy.isNotEmpty()) ", and the domain asks for " + v.dmarcPolicy else "")
            "none" -> if (v.dmarcAligned.isNotEmpty()) "DMARC: aligned (" + v.dmarcAligned + "), no policy published" else "DMARC: no policy published"
            "" -> "DMARC not checked"
            else -> "DMARC " + v.dmarc
        })
        out.add(when (v.reverse) {
            "ok" -> "reverse DNS ok" + (if (v.heloMatch) ", matches the server's name" else "")
            "none" -> "no reverse DNS for the sending server"
            "mismatch" -> "reverse DNS does not point back at the sending server"
            "relay" -> "from the relay itself"
            else -> "reverse DNS not checked"
        })
        if (v.screen.isNotEmpty()) out.add("headers: " + v.screen.joinToString("; "))
        return out
    }

    /** The one-line spam verdict with its reasons. */
    fun spamLine(v: Verdicts): String {
        val head = when (v.spam) { "high" -> "likely spam"; "medium" -> "possibly spam"; "low" -> "looks genuine"; else -> "not judged" }
        return if (v.spamReasons.isEmpty()) head else head + " · " + v.spamReasons.joinToString(", ")
    }

    /** Who a reply goes to: Reply-To when set, else the sender; reply-all adds everyone but us. */
    fun replyTo(r: Row, own: String, all: Boolean): Pair<List<String>, List<String>> {
        val me = own.lowercase()
        val to = ArrayList<String>()
        if (r.direction == "out") to.addAll(r.to) else to.add(r.replyTo.ifBlank { r.from })
        if (!all) return to.filter { it.isNotBlank() } to emptyList()
        val cc = ArrayList<String>()
        (r.to + r.cc).forEach { a ->
            val x = a.lowercase()
            if (x != me && x !in to && x !in cc && x.isNotBlank()) cc.add(x)
        }
        return to.filter { it.isNotBlank() && it.lowercase() != me }.ifEmpty { to } to cc
    }

    fun replySubject(s: String): String = if (s.lowercase().startsWith("re:")) s else "Re: " + s
    fun forwardSubject(s: String): String = if (s.lowercase().startsWith("fwd:") || s.lowercase().startsWith("fw:")) s else "Fwd: " + s

    fun bytes(n: Long): String = when {
        n < 1024 -> "$n B"
        n < 1024 * 1024 -> "%.0f KB".format(java.util.Locale.UK, n / 1024.0)
        else -> "%.1f MB".format(java.util.Locale.UK, n / 1048576.0)
    }

    fun attachmentLine(a: Attachment): String =
        if (a.kept) a.name + " · " + bytes(a.bytes) + (if (a.mime.isNotBlank()) " · " + a.mime else "")
        else a.name + " · not kept"

    /** The folder's empty line. */
    fun empty(folder: String, q: String): String = when {
        q.isNotBlank() -> "nothing matches \"$q\""
        folder == "sent" -> "nothing sent yet"
        folder == "archive" -> "nothing archived"
        else -> "no mail yet"
    }

    /** The counts line under the folder chips. */
    fun counts(inbox: Int, unseen: Int, sent: Int, waiting: Int): String {
        val p = ArrayList<String>()
        p.add(if (inbox == 1) "1 message" else "$inbox messages")
        if (unseen > 0) p.add("$unseen new")
        if (sent > 0) p.add("$sent sent")
        if (waiting > 0) p.add("$waiting waiting at the relay")
        return p.joinToString(" · ")
    }

    /** SETTINGS › MAIL's closed line. */
    fun settingsLine(address: String, relayHost: String, ready: Boolean, lastErr: String): String = when {
        address.isBlank() -> "not set up"
        !ready -> "$address · the relay is not set"
        lastErr.isNotBlank() -> "$address · $lastErr"
        relayHost.isNotBlank() -> "$address through $relayHost"
        else -> address
    }

    /** The status line on the settings page. */
    fun statusLine(ready: Boolean, lastPull: Long, lastOk: Long, lastErr: String, queued: Int, nowS: Long): String {
        if (!ready) return "set the domain, the relay and the box id, then paste the records and the enrol line"
        if (lastPull <= 0) return "waiting for the first pull (within two minutes of unlock)"
        val ago = NewsText.ago(lastOk, nowS)
        return when {
            lastErr.isNotBlank() && lastOk <= 0 -> "no pull has worked yet , $lastErr"
            lastErr.isNotBlank() -> "last good pull $ago , now: $lastErr"
            queued > 0 -> "pulled $ago · $queued still on the relay"
            else -> "pulled $ago"
        }
    }
}
