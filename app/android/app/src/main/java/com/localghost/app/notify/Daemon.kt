package com.localghost.app.notify

import android.graphics.Color
import com.localghost.app.R

/** Per-source identity. Small icon is the ghost silhouette (Android masks it to a tint). */
enum class Daemon(val id: String, val label: String, val color: Int) {
    FRAMED("ghost.framed", "ghost.framed", Color.parseColor("#33FF00")),
    VOICED("ghost.voiced", "ghost.voiced", Color.parseColor("#33FF00")),
    CUED("ghost.cued", "ghost.cued", Color.parseColor("#33FF00")),
    SHADOWD("ghost.shadowd", "ghost.shadowd", Color.parseColor("#FF8A8A")),
    WATCHD("ghost.watchd", "ghost.watchd", Color.parseColor("#A0A0A0")),
    NOTED("ghost.noted", "ghost.noted", Color.parseColor("#33FF00"));

    val icon: Int get() = R.drawable.ic_ghost_notif

    companion object { fun from(id: String) = entries.firstOrNull { it.id == id } ?: WATCHD }
}
