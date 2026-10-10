package com.localghost.app.ui

/** What the box says of its Met Office key: set or not, the order, the model's last pull (its
 *  run, why it is skipped, or its error), and where the box thinks home is for the order's
 *  region. Pure, so the tests read it; BoxClient fills it. */
data class MetOfficeState(val hasKey: Boolean, val order: String, val lastRun: String, val skipped: String, val error: String,
                          val homeKnown: Boolean, val homeNear: String, val homeRegion: String, val homeNights: Int, val homeNote: String)

/** The Met Office section's lines (pure, for the tests). */
object SettingsText {
    /** "no key yet", "key set · order my-order · last run 2026-10-10 06Z", with the error or why
     *  the box skips the model. */
    fun metOfficeLine(st: MetOfficeState?): String {
        if (st == null) return "the box did not answer"
        if (!st.hasKey) return "no key yet · the box forecasts from the free models (IFS, ICON, GFS) without it"
        val sb = StringBuilder("key set · order ").append(st.order)
        when {
            st.error.isNotEmpty() -> sb.append(" · ").append(st.error)
            st.lastRun.isNotEmpty() -> sb.append(" · last run ").append(st.lastRun)
            st.skipped.isNotEmpty() -> sb.append(" · ").append(st.skipped)
            else -> sb.append(" · not pulled yet")
        }
        return sb.toString()
    }

    /** Where the box thinks home is, for the order's region. */
    fun homeLine(st: MetOfficeState): String = when {
        st.homeKnown && st.homeNear.isNotEmpty() -> "home, by the trail's nights: near ${st.homeNear} · give the order a region of about ${st.homeRegion}"
        st.homeKnown -> "home, by the trail's nights: a region of about ${st.homeRegion}"
        else -> st.homeNote
    }
}
