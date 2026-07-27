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

    private fun write(ctx: Context, thread: Thread, t: Throwable) {
        val sw = StringWriter()
        PrintWriter(sw).use { t.printStackTrace(it) }
        val stamp = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date())
        File(ctx.filesDir, FILE).writeText(buildString {
            appendLine("LocalGhost crash report")
            appendLine("time:    $stamp")
            appendLine("thread:  ${thread.name}")
            appendLine("type:    ${t.javaClass.name}")
            appendLine("message: ${t.message}")
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
