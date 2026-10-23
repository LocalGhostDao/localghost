package com.localghost.app.ui

import org.json.JSONArray
import org.json.JSONObject
import java.time.LocalDate
import java.time.format.DateTimeFormatter
import java.util.Locale

/**
 * THE CONTACTS' words and shapes, pure: what the contacts provider's type numbers mean, a
 * birthday in the forms address books write it, the upload the box takes, the contacts as the
 * box gives them back, and the lines the CONTACTS page shows. No Android in here, so the tests
 * read it. Read-only, everywhere: nothing in the app writes to the address book.
 */
object ContactsText {
    data class Number(val number: String, val e164: String, val label: String)
    data class Email(val address: String, val label: String)

    data class Contact(
        val id: Long, val lookup: String, val name: String, val given: String, val family: String, val nickname: String,
        val org: String, val title: String, val starred: Boolean, val numbers: List<Number>, val emails: List<Email>, val birthday: String,
    )

    data class Meta(val at: Long, val contacts: Int, val removed: Int, val numbers: Int, val emails: Int, val birthdays: Int)

    data class Page(val contacts: List<Contact>, val meta: Meta?)

    // ── the provider's numbers (ContactsContract.CommonDataKinds) ──

    /** Phone.TYPE as a word; the custom label when the type is custom (0). */
    fun phoneLabel(type: Int?, custom: String?): String = when (type) {
        0 -> custom?.trim()?.lowercase(Locale.UK).orEmpty()
        1 -> "home"; 2 -> "mobile"; 3 -> "work"; 4 -> "work fax"; 5 -> "home fax"; 6 -> "pager"; 7 -> "other"
        9 -> "car"; 10 -> "company"; 12 -> "main"; 17 -> "work mobile"; 19 -> "assistant"
        else -> ""
    }

    /** Email.TYPE as a word. */
    fun emailLabel(type: Int?, custom: String?): String = when (type) {
        0 -> custom?.trim()?.lowercase(Locale.UK).orEmpty()
        1 -> "home"; 2 -> "work"; 3 -> "other"; 4 -> "mobile"
        else -> ""
    }

    /** Event.TYPE for a birthday. */
    const val BIRTHDAY = 3

    /** A birthday as YYYY-MM-DD, or --MM-DD without the year; "" when it is not one. The forms
     *  address books write: 1986-03-14, --03-14, 19860314, 14/03/1986, 14.03.1986. The box
     *  checks it again. */
    fun birthday(s: String?): String {
        val t = s?.trim().orEmpty()
        if (t.isEmpty()) return ""
        fun ok(y: Int, m: Int, d: Int): String {
            if (m !in 1..12 || d !in 1..31) return ""
            return if (y > 0) {
                if (y < 1880 || y > LocalDate.now().year + 1) return ""
                runCatching { LocalDate.of(y, m, d).toString() }.getOrDefault("")
            } else runCatching { LocalDate.of(2000, m, d); String.format(Locale.ROOT, "--%02d-%02d", m, d) }.getOrDefault("")
        }
        fun n(x: String) = x.toIntOrNull() ?: -1
        if (t.startsWith("--")) {
            val r = t.substring(2).replace("-", "")
            return if (r.length == 4) ok(0, n(r.substring(0, 2)), n(r.substring(2))) else ""
        }
        if (t.length >= 10 && t[4] == '-' && t[7] == '-') return ok(n(t.substring(0, 4)), n(t.substring(5, 7)), n(t.substring(8, 10)))
        if (t.length == 8 && t.all { it.isDigit() }) return ok(n(t.substring(0, 4)), n(t.substring(4, 6)), n(t.substring(6)))
        val f = t.split('/', '.', '-', ' ').filter { it.isNotEmpty() }
        if (f.size == 3 && f.all { p -> p.all { it.isDigit() } }) {
            return if (f[0].length == 4) ok(n(f[0]), n(f[1]), n(f[2])) else ok(n(f[2]), n(f[1]), n(f[0]))
        }
        return ""
    }

    // ── the upload ──

    fun contactJson(c: Contact): JSONObject {
        val o = JSONObject().put("id", c.id).put("name", c.name)
        if (c.lookup.isNotEmpty()) o.put("lookup", c.lookup)
        if (c.given.isNotEmpty()) o.put("given", c.given)
        if (c.family.isNotEmpty()) o.put("family", c.family)
        if (c.nickname.isNotEmpty()) o.put("nickname", c.nickname)
        if (c.org.isNotEmpty()) o.put("org", c.org)
        if (c.title.isNotEmpty()) o.put("title", c.title)
        if (c.starred) o.put("starred", true)
        if (c.numbers.isNotEmpty()) {
            val a = JSONArray()
            c.numbers.forEach { n ->
                val x = JSONObject().put("number", n.number)
                if (n.e164.isNotEmpty()) x.put("e164", n.e164)
                if (n.label.isNotEmpty()) x.put("label", n.label)
                a.put(x)
            }
            o.put("numbers", a)
        }
        if (c.emails.isNotEmpty()) {
            val a = JSONArray()
            c.emails.forEach { e ->
                val x = JSONObject().put("address", e.address)
                if (e.label.isNotEmpty()) x.put("label", e.label)
                a.put(x)
            }
            o.put("emails", a)
        }
        if (c.birthday.isNotEmpty()) o.put("birthday", c.birthday)
        return o
    }

    /** The body POST /v1/contacts/upload takes, sorted so the same contacts are the same bytes. */
    fun upload(device: String, contacts: List<Contact>): JSONObject {
        val a = JSONArray()
        contacts.sortedWith(compareBy({ it.lookup }, { it.id })).forEach { a.put(contactJson(it)) }
        return JSONObject().put("device", device).put("contacts", a)
    }

    fun fingerprint(body: JSONObject): String {
        val s = body.optJSONArray("contacts")?.toString() ?: ""
        val d = java.security.MessageDigest.getInstance("SHA-256").digest(s.toByteArray())
        return d.take(12).joinToString("") { String.format(Locale.ROOT, "%02x", it) }
    }

    // ── what the box gives back ──

    fun parseContact(o: JSONObject): Contact {
        val ns = ArrayList<Number>()
        o.optJSONArray("numbers")?.let { a -> for (i in 0 until a.length()) a.optJSONObject(i)?.let { x -> ns.add(Number(x.optString("number"), x.optString("e164"), x.optString("label"))) } }
        val es = ArrayList<Email>()
        o.optJSONArray("emails")?.let { a -> for (i in 0 until a.length()) a.optJSONObject(i)?.let { x -> es.add(Email(x.optString("address"), x.optString("label"))) } }
        return Contact(o.optLong("id"), o.optString("lookup"), o.optString("name"), o.optString("given"), o.optString("family"),
            o.optString("nickname"), o.optString("org"), o.optString("title"), o.optBoolean("starred"), ns, es, o.optString("birthday"))
    }

    fun parseMeta(o: JSONObject?): Meta? {
        if (o == null || o.optLong("at") <= 0) return null
        return Meta(o.optLong("at"), o.optInt("contacts"), o.optInt("removed"), o.optInt("numbers"), o.optInt("emails"), o.optInt("birthdays"))
    }

    fun parsePage(o: JSONObject): Page {
        val a = o.optJSONArray("contacts")
        val cs = if (a == null) emptyList() else (0 until a.length()).mapNotNull { a.optJSONObject(it)?.let(::parseContact) }
        return Page(cs, parseMeta(o.optJSONObject("meta")))
    }

    // ── the lines ──

    fun name(c: Contact): String = c.name.trim().ifEmpty { (c.given + " " + c.family).trim() }.ifEmpty {
        c.numbers.firstOrNull()?.number ?: c.emails.firstOrNull()?.address ?: "(no name)"
    }

    /** The second line: what they are, else their first number or address. */
    fun second(c: Contact): String {
        val what = listOf(c.title, c.org).filter { it.isNotBlank() }.joinToString(", ")
        if (what.isNotEmpty()) return what
        c.numbers.firstOrNull()?.let { return it.number }
        c.emails.firstOrNull()?.let { return it.address }
        return ""
    }

    /** The list's index letter: the first letter of the name, "#" for anything else. */
    fun letter(c: Contact): String {
        val ch = name(c).firstOrNull { it.isLetterOrDigit() } ?: return "#"
        return if (ch.isLetter()) ch.uppercaseChar().toString() else "#"
    }

    private val dm = DateTimeFormatter.ofPattern("d MMMM", Locale.UK)
    private val dmy = DateTimeFormatter.ofPattern("d MMMM yyyy", Locale.UK)

    /** "14 March 1986", "14 March"; and the age on [today] when the year is known. */
    fun birthdayLine(b: String, today: LocalDate): String {
        if (b.startsWith("--") && b.length == 7) {
            return runCatching { LocalDate.of(2000, b.substring(2, 4).toInt(), b.substring(5, 7).toInt()).format(dm) }.getOrDefault("")
        }
        val d = runCatching { LocalDate.parse(b) }.getOrNull() ?: return ""
        var age = today.year - d.year
        if (today.monthValue < d.monthValue || (today.monthValue == d.monthValue && today.dayOfMonth < d.dayOfMonth)) age--
        return d.format(dmy) + if (age in 1..130) " · $age" else ""
    }

    /** SETTINGS' and INTEGRATIONS' line for the last sync. */
    fun synced(m: Meta?, nowSec: Long): String {
        if (m == null) return "the box has no contacts from this phone yet"
        val ago = nowSec - m.at
        val w = when {
            ago < 90 -> "just now"
            ago < 3600 -> "${ago / 60} min ago"
            ago < 86400 -> "${ago / 3600} h ago"
            else -> "${ago / 86400} days ago"
        }
        return "synced $w · ${m.contacts} contacts, ${m.birthdays} with a birthday"
    }
}
