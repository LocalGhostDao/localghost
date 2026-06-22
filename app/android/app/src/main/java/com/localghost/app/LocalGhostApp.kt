package com.localghost.app

import android.app.Application
import com.localghost.app.debug.CrashHandler

class LocalGhostApp : Application() {
    override fun onCreate() {
        super.onCreate()
        CrashHandler.install(this)
    }
}
