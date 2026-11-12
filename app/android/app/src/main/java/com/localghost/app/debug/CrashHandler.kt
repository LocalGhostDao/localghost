package com.localghost.app.debug

import android.content.Context
import android.util.Log
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

object CrashHandler {
    private const val FILE = "last_crash.txt"

    fun install(ctx: Context) {
        val appCtx = ctx.applicationContext
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            Log.e("LocalGhost", "uncaught exception on ${thread.name}", throwable)
            try { write(appCtx, thread, throwable) } catch (_: Throwable) {}
            previous?.uncaughtException(thread, throwable)
        }
    }

    /**
     * The report is kept in the app's files and shown BEFORE the gate, so it says where the
     * crash happened and not what was being read: an exception's message can carry what it
     * choked on (a JSON parser quotes its input), so each message is one line, cut short, and
     * the frames are the app's and the platform's names only.
     */
    internal fun trace(t: Throwable): String {
        val sb = StringBuilder()
        var cur: Throwable? = t
        var depth = 0
        while (cur != null && depth < 6) {
            if (depth > 0) sb.append("Caused by: ")
            sb.append(cur.javaClass.name)
            oneLine(cur.message)?.let { sb.append(": ").append(it) }
            sb.append('\n')
            for (f in cur.stackTrace.take(40)) sb.append("\tat ").append(f.toString()).append('\n')
            if (cur.stackTrace.size > 40) sb.append("\t… ").append(cur.stackTrace.size - 40).append(" more\n")
            cur = cur.cause.takeIf { it !== cur }
            depth++
        }
        return sb.toString()
    }

    internal fun oneLine(message: String?): String? {
        val m = message?.replace(Regex("\\s+"), " ")?.trim().orEmpty()
        if (m.isEmpty()) return null
        return if (m.length <= 160) m else m.take(160) + "… (" + m.length + " characters)"
    }

    private fun write(ctx: Context, thread: Thread, t: Throwable) {
        val sw = StringWriter()
        PrintWriter(sw).use { it.print(trace(t)) }
        val stamp = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date())
        File(ctx.filesDir, FILE).writeText(buildString {
            appendLine("LocalGhost crash report")
            appendLine("time:    $stamp")
            appendLine("thread:  ${thread.name}")
            appendLine("type:    ${t.javaClass.name}")
            appendLine("message: ${oneLine(t.message) ?: ""}")
            appendLine("device:  ${android.os.Build.MANUFACTURER} ${android.os.Build.MODEL}")
            appendLine("android: API ${android.os.Build.VERSION.SDK_INT}")
            appendLine()
            append(sw.toString())
        })
    }

    fun pending(ctx: Context): String? =
        File(ctx.filesDir, FILE).let { if (it.exists()) it.readText() else null }

    fun clear(ctx: Context) { File(ctx.filesDir, FILE).delete() }
}
