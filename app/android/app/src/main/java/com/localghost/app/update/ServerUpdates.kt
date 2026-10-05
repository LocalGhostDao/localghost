package com.localghost.app.update

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * NEW SERVER RELEASES, FROM THE PHONE. The box never looks for updates: it does not reach the
 * internet after setup. The phone does, once a day on Wi-Fi: it reads the mirror's manifest (the
 * same signed list every download at setup came from) and, when a newer server release is there,
 * says so once. DEPLOY downloads the set on Wi-Fi and hands it to the box, which checks the
 * signature and every hash with the key it already holds before anything changes, puts the
 * release on, and restarts onto it (the app locks; you unlock again). The release is on trial
 * until its first unlock has run ten minutes with the daemons up, and goes back by itself if it
 * fails (secd update_http.go, internal/update).
 *
 * What this tells the mirror: that a phone fetched a public manifest, as any visitor would. Nothing
 * of the box's or the person's goes with it.
 */
object ServerUpdates {
    const val MIRROR = "https://www.localghost.ai/mirror"
    private const val PREFS = "lg_server_update"
    private const val WORK = "localghost.server-update"

    /** A release the mirror offers: its notes, the build it is in, and the set's files. */
    data class Offer(val release: ReleaseInfo.Release, val build: String, val files: List<ReleaseInfo.SetFile>)

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** The version the box said it runs, the last time the app asked (after an unlock). */
    fun boxVersion(ctx: Context): String = prefs(ctx).getString("box", "") ?: ""
    fun noteBoxVersion(ctx: Context, v: String) { prefs(ctx).edit().putString("box", v).apply() }

    /** The last offer read from the mirror, or null. */
    fun lastOffer(ctx: Context): Offer? {
        val p = prefs(ctx)
        val rel = ReleaseInfo.release(p.getString("release", "") ?: "") ?: return null
        val man = ReleaseInfo.manifest(p.getString("manifest", "") ?: "") ?: return null
        return Offer(rel, man.build, man.server)
    }

    /** Why the last check found nothing: the mirror unreachable, no server set in its build, or a
     *  release that did not read. "" after a check that found one. */
    fun lastMiss(ctx: Context): String = prefs(ctx).getString("miss", "") ?: ""

    /** Reads the mirror (manifest, then the release notes). Null when the mirror has no server set
     *  or could not be reached ([lastMiss] says which). Blocking network: call it off the main thread. */
    suspend fun check(ctx: Context): Offer? = withContext(Dispatchers.IO) {
        val miss = { why: String -> prefs(ctx).edit().putString("miss", why).apply(); null }
        // the manifest is read line by line and only the header and the server set kept: it lists
        // every file of every set (the elevation tiles alone are tens of thousands of lines)
        val manText = fetchLines("$MIRROR/MANIFEST.txt", ReleaseInfo::keep) ?: return@withContext miss("the mirror did not answer")
        val man = ReleaseInfo.manifest(manText) ?: return@withContext miss("the mirror's manifest did not read")
        val notes = man.server.firstOrNull { it.name == "RELEASE.txt" }
            ?: return@withContext miss("the mirror's build ${man.build} has no server release in it")
        val relBytes = fetchBytes(MIRROR + notes.path) ?: return@withContext miss("the release notes did not download")
        if (sha256(relBytes) != notes.sha256) return@withContext miss("the release notes are not what the manifest says")
        val relText = String(relBytes)
        val rel = ReleaseInfo.release(relText) ?: return@withContext miss("the release notes did not read")
        prefs(ctx).edit().putString("manifest", manText).putString("release", relText).putString("miss", "")
            .putLong("checked", System.currentTimeMillis()).apply()
        Offer(rel, man.build, man.server)
    }

    /** The offer, when it is newer than what the box runs. */
    fun pending(ctx: Context): Offer? {
        val o = lastOffer(ctx) ?: return null
        val box = boxVersion(ctx)
        return if (box.isNotEmpty() && ReleaseInfo.newer(o.release.version, box)) o else null
    }

    /**
     * Downloads the offer's set on this phone (each file checked against the manifest) and hands it
     * to the box, then asks the box to put it on. The box's answer: ok, or why not. Blocking.
     */
    suspend fun deploy(ctx: Context, o: Offer, progress: (String) -> Unit): Pair<Boolean, String> = withContext(Dispatchers.IO) {
        val dir = File(ctx.cacheDir, "server-update").apply { deleteRecursively(); mkdirs() }
        try {
            val man = File(dir, "MANIFEST.txt")
            val asc = File(dir, "MANIFEST.txt.asc")
            progress("reading the mirror's manifest…")
            if (!fetchFile("$MIRROR/MANIFEST.txt", man) || !fetchFile("$MIRROR/MANIFEST.txt.asc", asc))
                return@withContext false to "the mirror did not answer"
            val now = ReleaseInfo.manifest(man.readText()) ?: return@withContext false to "the mirror's manifest did not read"
            // a site deploy makes a new build of the mirror with the same files: what matters is that
            // the release is the one the person was shown
            val notesNow = now.server.firstOrNull { it.name == "RELEASE.txt" }?.sha256
            val notesThen = o.files.firstOrNull { it.name == "RELEASE.txt" }?.sha256
            if (notesNow == null || notesNow != notesThen) return@withContext false to "the mirror offers a different release since; check again"
            val files = ArrayList<Pair<ReleaseInfo.SetFile, File>>()
            for (f in now.server) {
                val local = File(dir, f.name)
                progress("downloading ${f.name}…")
                if (!fetchFile(MIRROR + f.path, local)) return@withContext false to "${f.name} did not download"
                if (sha256(local) != f.sha256) return@withContext false to "${f.name} is not what the manifest says"
                files.add(f to local)
            }
            // to the box: the manifest first (it starts a new upload), its signature, then the set
            progress("handing it to your box…")
            if (!BoxClient.updateUpload(ctx, "MANIFEST.txt", man)) return@withContext false to "the box did not take the manifest"
            if (!BoxClient.updateUpload(ctx, "MANIFEST.txt.asc", asc)) return@withContext false to "the box did not take the signature"
            for ((f, local) in files) {
                progress("handing ${f.name} to your box…")
                if (!BoxClient.updateUpload(ctx, "${now.build}/server/${f.name}", local)) return@withContext false to "the box did not take ${f.name}"
            }
            progress("your box is checking the signature and putting it on…")
            BoxClient.updateApply(ctx)
        } finally {
            dir.deleteRecursively()
        }
    }

    // --- once a day ---

    fun schedule(ctx: Context) {
        val req = PeriodicWorkRequestBuilder<ServerUpdateWorker>(1, TimeUnit.DAYS)
            .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.UNMETERED).setRequiresBatteryNotLow(true).build())
            .build()
        WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(WORK, ExistingPeriodicWorkPolicy.KEEP, req)
    }

    /** Says a new release is out, once per version. */
    internal fun notifyOnce(ctx: Context) {
        val o = pending(ctx) ?: return
        val p = prefs(ctx)
        if (p.getString("notified", "") == o.release.version) return
        p.edit().putString("notified", o.release.version).apply()
        com.localghost.app.notify.Notifications.postServerRelease(ctx, o.release.label, o.release.changes.size)
    }

    // --- plain https to the mirror (the box is not involved) ---

    private fun open(url: String): HttpURLConnection =
        (URL(url).openConnection() as HttpURLConnection).apply {
            connectTimeout = 15_000; readTimeout = 60_000
            instanceFollowRedirects = true
            setRequestProperty("Cache-Control", "no-cache")
        }

    /** A small text file (a release's notes), whole, up to a megabyte. */
    private fun fetchBytes(url: String): ByteArray? = try {
        val c = open(url)
        try { if (c.responseCode == 200) c.inputStream.use { it.readBytes().take(1 shl 20).toByteArray() } else null } finally { c.disconnect() }
    } catch (e: Exception) { null }

    /** A text file line by line, keeping the lines [keep] says, up to 64 MB read. */
    private fun fetchLines(url: String, keep: (String) -> Boolean): String? = try {
        val c = open(url)
        try {
            if (c.responseCode != 200) null
            else c.inputStream.bufferedReader().use { r ->
                val sb = StringBuilder()
                var read = 0L
                while (true) {
                    val l = r.readLine() ?: break
                    read += l.length + 1
                    if (read > (64L shl 20)) break
                    if (keep(l)) sb.append(l).append('\n')
                }
                sb.toString()
            }
        } finally { c.disconnect() }
    } catch (e: Exception) { null }

    private fun fetchFile(url: String, dst: File): Boolean = try {
        val c = open(url)
        try {
            if (c.responseCode != 200) false
            else { c.inputStream.use { ins -> dst.outputStream().use { ins.copyTo(it, 256 * 1024) } }; true }
        } finally { c.disconnect() }
    } catch (e: Exception) { false }

    private fun sha256(b: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

    private fun sha256(f: File): String {
        val md = MessageDigest.getInstance("SHA-256")
        f.inputStream().use { ins ->
            val buf = ByteArray(256 * 1024)
            while (true) { val n = ins.read(buf); if (n < 0) break; md.update(buf, 0, n) }
        }
        return md.digest().joinToString("") { "%02x".format(it) }
    }
}

/** Once a day on Wi-Fi: read the mirror, and say once when a newer server release is out. */
class ServerUpdateWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        ServerUpdates.check(applicationContext)
        ServerUpdates.notifyOnce(applicationContext)
        return Result.success()
    }
}
