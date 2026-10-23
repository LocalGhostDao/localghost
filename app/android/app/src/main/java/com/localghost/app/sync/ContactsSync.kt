package com.localghost.app.sync

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.provider.ContactsContract
import android.telephony.PhoneNumberUtils
import android.telephony.TelephonyManager
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.ContactsText
import java.util.Locale
import java.util.concurrent.TimeUnit

/**
 * THE CONTACTS, read here and kept on the box. Android's contacts provider holds every account
 * the phone syncs (Google's, Samsung's, the SIM's) merged into one person each; this reads each
 * person's names, numbers, addresses, organisation and birthday (not the photo, not the notes)
 * and posts the whole set to the box, which replaces what it held. No contacts service is asked
 * anything and nothing is ever written to the address book. Sent when it changed: the provider
 * says when it did (a content trigger), every six hours besides, and on unlock when the last
 * read is an hour old.
 */
object ContactsSync {
    const val PERMISSION = Manifest.permission.READ_CONTACTS
    private const val PREFS = "lg_contacts"
    private const val DEVICE = "phone" // one phone per box: a new phone's contacts replace the old one's

    fun granted(ctx: Context): Boolean = ctx.checkSelfPermission(PERMISSION) == PackageManager.PERMISSION_GRANTED

    fun enabled(ctx: Context): Boolean = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean("on", true)

    fun setEnabled(ctx: Context, on: Boolean) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean("on", on).putString("hash", "").apply()
        if (on) schedule(ctx) else {
            val wm = androidx.work.WorkManager.getInstance(ctx)
            wm.cancelUniqueWork("localghost.contacts"); wm.cancelUniqueWork("localghost.contacts.change")
        }
    }

    data class LastRun(val at: Long, val contacts: Int, val error: String)

    fun lastRun(ctx: Context): LastRun? {
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val at = p.getLong("at", 0L)
        if (at <= 0) return null
        return LastRun(at, p.getInt("contacts", 0), p.getString("error", "") ?: "")
    }

    private fun note(ctx: Context, contacts: Int, error: String, hash: String?) {
        val e = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putLong("at", System.currentTimeMillis() / 1000).putInt("contacts", contacts).putString("error", error)
        if (hash != null) e.putLong("sentAt", System.currentTimeMillis() / 1000).putString("hash", hash)
        e.apply()
    }

    /** The country numbers without a + are read in: the SIM's, else the network's, else the phone's language's. */
    private fun country(ctx: Context): String {
        val tm = ctx.getSystemService(Context.TELEPHONY_SERVICE) as? TelephonyManager
        val c = tm?.simCountryIso?.takeIf { it.isNotBlank() } ?: tm?.networkCountryIso?.takeIf { it.isNotBlank() } ?: Locale.getDefault().country
        return c.uppercase(Locale.ROOT)
    }

    private class Building(val id: Long, val lookup: String, val name: String, val starred: Boolean) {
        var given = ""; var family = ""; var nickname = ""; var org = ""; var title = ""; var birthday = ""
        val numbers = ArrayList<ContactsText.Number>()
        val emails = ArrayList<ContactsText.Email>()
        val seenDigits = HashSet<String>()
        val seenMail = HashSet<String>()
    }

    /** Every contact, built from the provider's data rows (one row per name, number, address…). */
    fun read(ctx: Context): List<ContactsText.Contact> {
        val cc = country(ctx)
        val kinds = listOf(
            ContactsContract.CommonDataKinds.StructuredName.CONTENT_ITEM_TYPE, ContactsContract.CommonDataKinds.Phone.CONTENT_ITEM_TYPE,
            ContactsContract.CommonDataKinds.Email.CONTENT_ITEM_TYPE, ContactsContract.CommonDataKinds.Event.CONTENT_ITEM_TYPE,
            ContactsContract.CommonDataKinds.Organization.CONTENT_ITEM_TYPE, ContactsContract.CommonDataKinds.Nickname.CONTENT_ITEM_TYPE,
        )
        val cols = arrayOf(ContactsContract.Data.CONTACT_ID, ContactsContract.Data.LOOKUP_KEY, ContactsContract.Data.DISPLAY_NAME,
            ContactsContract.Data.STARRED, ContactsContract.Data.MIMETYPE, ContactsContract.Data.DATA1, ContactsContract.Data.DATA2,
            ContactsContract.Data.DATA3, ContactsContract.Data.DATA4)
        val sel = ContactsContract.Data.MIMETYPE + " IN (" + kinds.joinToString(",") { "?" } + ")"
        val by = LinkedHashMap<Long, Building>()
        ctx.contentResolver.query(ContactsContract.Data.CONTENT_URI, cols, sel, kinds.toTypedArray(), ContactsContract.Data.CONTACT_ID + " ASC")?.use { c ->
            fun s(i: Int): String = if (c.isNull(i)) "" else (c.getString(i) ?: "").trim()
            fun n(i: Int): Int? = if (c.isNull(i)) null else c.getString(i)?.toIntOrNull()
            while (c.moveToNext()) {
                val id = c.getLong(0)
                val b = by.getOrPut(id) { Building(id, s(1), s(2).take(200), !c.isNull(3) && c.getInt(3) == 1) }
                when (s(4)) {
                    ContactsContract.CommonDataKinds.StructuredName.CONTENT_ITEM_TYPE -> {
                        if (b.given.isEmpty()) b.given = s(6).take(100)
                        if (b.family.isEmpty()) b.family = s(7).take(100)
                    }
                    ContactsContract.CommonDataKinds.Phone.CONTENT_ITEM_TYPE -> {
                        val raw = s(5)
                        if (raw.isEmpty() || b.numbers.size >= 20) continue
                        val e164 = s(8).ifEmpty { runCatching { PhoneNumberUtils.formatNumberToE164(raw, cc) }.getOrNull() ?: "" }
                        val key = (e164.ifEmpty { raw }).filter { it.isDigit() }.takeLast(9)
                        if (key.isEmpty() || !b.seenDigits.add(key)) continue
                        b.numbers.add(ContactsText.Number(raw.take(40), e164.take(20), ContactsText.phoneLabel(n(6), s(7)).take(40)))
                    }
                    ContactsContract.CommonDataKinds.Email.CONTENT_ITEM_TYPE -> {
                        val a = s(5)
                        if (!a.contains('@') || b.emails.size >= 20 || !b.seenMail.add(a.lowercase(Locale.ROOT))) continue
                        b.emails.add(ContactsText.Email(a.take(200), ContactsText.emailLabel(n(6), s(7)).take(40)))
                    }
                    ContactsContract.CommonDataKinds.Event.CONTENT_ITEM_TYPE ->
                        if (n(6) == ContactsText.BIRTHDAY && b.birthday.isEmpty()) b.birthday = ContactsText.birthday(s(5))
                    ContactsContract.CommonDataKinds.Organization.CONTENT_ITEM_TYPE -> {
                        if (b.org.isEmpty()) b.org = s(5).take(200)
                        if (b.title.isEmpty()) b.title = s(8).take(200)
                    }
                    ContactsContract.CommonDataKinds.Nickname.CONTENT_ITEM_TYPE -> if (b.nickname.isEmpty()) b.nickname = s(5).take(100)
                }
            }
        }
        return by.values.filter { it.name.isNotEmpty() || it.numbers.isNotEmpty() || it.emails.isNotEmpty() }.map { b ->
            ContactsText.Contact(b.id, b.lookup, b.name, b.given, b.family, b.nickname, b.org, b.title, b.starred, b.numbers, b.emails, b.birthday)
        }
    }

    data class Result(val contacts: Int, val removed: Int, val skipped: Boolean, val error: String = "")

    /** Reads the contacts and sends them when they differ from the last set sent (or that one is
     *  a day old); force sends regardless. */
    suspend fun sync(ctx: Context, force: Boolean = false): Result {
        if (!granted(ctx)) return Result(0, 0, false, "no permission to read the contacts")
        val cs = try { read(ctx) } catch (e: Exception) {
            note(ctx, 0, "the contacts would not read: " + (e.message ?: e.javaClass.simpleName), null)
            return Result(0, 0, false, "the contacts would not read")
        }
        val body = ContactsText.upload(DEVICE, cs)
        val hash = ContactsText.fingerprint(body)
        val now = System.currentTimeMillis() / 1000
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        if (!force && hash == p.getString("hash", "") && now - p.getLong("sentAt", 0L) < 86400) {
            note(ctx, cs.size, "", null)
            return Result(cs.size, 0, true)
        }
        val r = BoxClient.contactsUpload(ctx, body)
        return if (r != null) {
            note(ctx, r.first, "", hash)
            Result(r.first, r.second, false)
        } else {
            note(ctx, cs.size, "the box did not take them", null)
            Result(cs.size, 0, false, "the box did not take them")
        }
    }

    suspend fun syncIfDue(ctx: Context) {
        if (!enabled(ctx) || !granted(ctx) || !com.localghost.app.security.BoxConfig.isConfigured(ctx)) return
        val last = lastRun(ctx)
        if (last != null && System.currentTimeMillis() / 1000 - last.at < 3600) return
        sync(ctx)
    }

    fun schedule(ctx: Context) {
        if (!enabled(ctx) || !granted(ctx)) return
        val periodic = androidx.work.PeriodicWorkRequestBuilder<ContactsWorker>(6, TimeUnit.HOURS)
            .setConstraints(androidx.work.Constraints.Builder().setRequiresBatteryNotLow(true).build()).build()
        androidx.work.WorkManager.getInstance(ctx).enqueueUniquePeriodicWork("localghost.contacts", androidx.work.ExistingPeriodicWorkPolicy.KEEP, periodic)
        watch(ctx)
    }

    /** A run a minute after the provider changes (a content trigger lasts one change; each run
     *  puts the next watch behind itself). */
    fun watch(ctx: Context, policy: androidx.work.ExistingWorkPolicy = androidx.work.ExistingWorkPolicy.KEEP) {
        val c = androidx.work.Constraints.Builder()
            .addContentUriTrigger(ContactsContract.Contacts.CONTENT_URI, true)
            .setTriggerContentUpdateDelay(60, TimeUnit.SECONDS)
            .setTriggerContentMaxDelay(10, TimeUnit.MINUTES)
            .build()
        val req = androidx.work.OneTimeWorkRequestBuilder<ContactsWorker>().setConstraints(c).build()
        androidx.work.WorkManager.getInstance(ctx).enqueueUniqueWork("localghost.contacts.change", policy, req)
    }
}

/** The background contacts sync, when the permission is there and a box is enrolled. */
class ContactsWorker(ctx: Context, params: androidx.work.WorkerParameters) : androidx.work.CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        if (!ContactsSync.enabled(ctx) || !ContactsSync.granted(ctx) || !com.localghost.app.security.BoxConfig.isConfigured(ctx)) return Result.success()
        runCatching { ContactsSync.sync(ctx) }
        if (triggeredContentUris.isNotEmpty() || triggeredContentAuthorities.isNotEmpty())
            ContactsSync.watch(ctx, androidx.work.ExistingWorkPolicy.APPEND_OR_REPLACE)
        return Result.success()
    }
}
