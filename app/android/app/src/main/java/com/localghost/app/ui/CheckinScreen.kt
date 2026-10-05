package com.localghost.app.ui

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import com.localghost.app.checkin.Feelings
import com.localghost.app.net.BoxClient
import com.localghost.app.settings.AppSettings
import com.localghost.app.ui.theme.*
import com.localghost.app.voice.VoiceCapture
import com.localghost.app.voice.VoiceNotes
import com.localghost.app.voice.VoicePlayback
import kotlinx.coroutines.launch

/**
 * THE DAILY CHECK-IN, a page of its own (it was a card in the middle of MEMORIES, between the note
 * about me and the voice notes, and the history was a list of lines behind a toggle).
 *
 * Top to bottom: the day; the last two weeks as a strip, one cell a day, each marked by the
 * quadrant the day was told by (▲ bright, ● easy, ◆ tense, ▼ heavy, ◇ mind, · nothing), a tap on a
 * cell opens that day's check-in; then today's: the feelings laid out as the mood meter's four
 * quadrants with the mind row under them, the box's guesses already ticked and marked, a why
 * prefilled from the day, a voice note, and the save. Once saved the page is what was said, the
 * day as the box tells it, and a recorder for more. Under that the past check-ins as rows (a tap
 * opens one), what recurred this month, and every voice note on the box.
 *
 * It all still lands in the JOURNAL (/v1/notes and /v1/voice), where synthd distils it with the
 * rest; nothing about the storage moved, only the page. No streaks, no counts of missed days: an
 * empty day is a dot. The box asks once in the evening and is silent after (secd's rule).
 */
@Composable
fun CheckinScreen(onOpenDay: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val today = remember { DayText.of(System.currentTimeMillis() / 1000) }
    var history by remember { mutableStateOf<List<BoxClient.CheckinRow>?>(null) }
    var voiceLocal by remember { mutableStateOf<List<VoiceNotes.Pending>>(emptyList()) }
    // a past check-in's page, by its day ("" for today's page)
    var past by rememberSaveable { mutableStateOf("") }
    var voiceOpen by rememberSaveable { mutableStateOf(false) }
    var voiceList by remember { mutableStateOf<List<BoxClient.VoiceNoteRow>?>(null) }
    // this visit saved the check-in: the day is written up now, once (a later visit reads it)
    var justSaved by remember { mutableStateOf(false) }
    fun reloadVoice() {
        voiceLocal = VoiceNotes.pending(ctx)
        scope.launch { voiceList = BoxClient.voiceNotes(ctx) }
    }
    fun reload() {
        voiceLocal = VoiceNotes.pending(ctx)
        scope.launch { history = BoxClient.checkins(ctx, 90) ?: history ?: emptyList() }
        if (voiceOpen) reloadVoice()
    }
    LaunchedEffect(Unit) {
        // what waits on the phone goes first, so the box's rows show it
        VoiceNotes.uploadPending(ctx)
        reload()
    }

    val rows = history ?: emptyList()
    val byDay = remember(rows) { rows.associateBy { it.day } }
    val todayRow = byDay[today]
    val onPhone = voiceLocal.map { it.id }.toSet()

    // ONE PAST CHECK-IN
    past.takeIf { it.isNotEmpty() }?.let { d ->
        val row = byDay[d]
        PastCheckin(d, today, row, onPhone, onBack = { past = "" }, onOpenDay = { onOpenDay(d) })
        return
    }

    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Text(DayText.heading(today), color = GhostText, style = MaterialTheme.typography.titleLarge)
        Text("how are you feeling today, and why · kept on your box", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(14.dp))

        // THE STRIP, the last two weeks
        val tones = remember(rows) { rows.associate { it.day to Feelings.tone(it.feelings, it.preselected) } }
        val cells = remember(tones, today) { Feelings.strip(today, tones, 14) }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            cells.forEach { c ->
                val isToday = c.day == today
                Column(Modifier.weight(1f)
                    .border(1.dp, if (isToday) TerminalGreen else if (c.checked) GhostBorder else VoidLighter, RectangleShape)
                    .background(if (c.checked) VoidLighter else Void)
                    .clickable(enabled = c.checked && !isToday) { past = c.day }
                    .padding(vertical = 6.dp),
                    horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(Feelings.mark(c.tone), color = toneColour(c.tone, c.checked), style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center)
                    Text(Feelings.dayNumber(c.day), color = if (isToday) TerminalGreen else TerminalDim, style = MaterialTheme.typography.labelSmall, textAlign = TextAlign.Center)
                }
            }
        }
        Text("▲ bright  ● easy  ◆ tense  ▼ heavy  ◇ mind · a tap opens the day", color = TerminalDim, style = MaterialTheme.typography.labelSmall,
            modifier = Modifier.padding(top = 4.dp))
        Spacer(Modifier.height(16.dp))

        // TODAY
        val done = todayRow != null || AppSettings.lastCheckinDay(ctx) == today
        if (!done && history != null) {
            rows.firstOrNull { it.day != today }?.let { y ->
                val picks = Feelings.picks(y.feelings)
                if (picks.isNotEmpty()) {
                    Text("${DayText.ago(y.day, today)} you felt ${picks.joinToString(", ")}", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.height(8.dp))
                }
            }
        }
        if (done) {
            CheckedIn(today, todayRow, onPhone, voiceLocal.filter { it.kind == "checkin" && it.day == today },
                justSaved = justSaved, onSaved = { reload() })
        } else {
            CheckinForm(today, rows, onSaved = { justSaved = true; reload() })
        }

        // THE PAST
        val older = rows.filter { it.day != today }
        if (older.isNotEmpty()) {
            Spacer(Modifier.height(24.dp))
            SectionLabel("PAST CHECK-INS")
            Spacer(Modifier.height(6.dp))
            val month = Feelings.recurring(older.filter { it.day >= DayText.shift(today, -30) }.map { it.feelings })
            if (month.isNotEmpty()) {
                Text("this month · $month", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(8.dp))
            }
            older.forEach { r ->
                val tone = Feelings.tone(r.feelings, r.preselected)
                val picks = Feelings.picks(r.feelings)
                Row(Modifier.fillMaxWidth().clickable { past = r.day }.padding(vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(Feelings.mark(tone), color = toneColour(tone, true), style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(22.dp))
                    Text(Feelings.shortDay(r.day), color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(84.dp))
                    Text(if (picks.isEmpty()) "(no feeling picked)" else picks.joinToString(", "), color = GhostText,
                        style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f), maxLines = 1)
                    if (r.voices.isNotEmpty()) Text(if (r.voices.size > 1) "🎙${r.voices.size}" else "🎙", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(end = 6.dp))
                    Text("›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                }
            }
        } else if (history != null && todayRow == null) {
            Spacer(Modifier.height(24.dp))
            Text("no check-ins yet · the first one starts the strip", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }

        // VOICE NOTES, everything said, newest first: what still waits on the phone, then the box's
        // notes with their transcripts. Deleting one removes the audio, the words and the journal
        // entry on the box.
        Spacer(Modifier.height(24.dp))
        val count = (voiceList?.size ?: 0) + voiceLocal.size
        Text(if (voiceOpen) "[ − voice notes ]" else "[ + voice notes" + (if (count > 0) " ($count)" else "") + " · what you said, transcribed on your box ]",
            color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                voiceOpen = !voiceOpen
                if (voiceOpen) reloadVoice()
            })
        if (voiceOpen) Column(Modifier.animateContentSize()) {
            voiceLocal.forEach { p ->
                key("local-" + p.id) {
                    VoiceNoteCard(BoxClient.VoiceNoteRow(p.id, p.kind, p.day, p.takenAt / 1000, p.durationMs, "missing", "", "", ""),
                        onPhone = true, onDelete = { VoiceNotes.deleteLocal(ctx, p.id); reloadVoice() })
                }
            }
            val list = voiceList
            when {
                list == null -> Text("asking the box…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                list.isEmpty() && voiceLocal.isEmpty() -> Text("none yet · record one with the check-in above",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                else -> list.forEach { v ->
                    key("box-" + v.id) {
                        VoiceNoteCard(v, onPhone = false, onDelete = {
                            scope.launch { BoxClient.voiceDelete(ctx, v.id); reload(); reloadVoice() }
                        })
                    }
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

/** The colour a quadrant's mark takes: the pleasant ones green, tense amber, heavy and mind dim. */
private fun toneColour(tone: String, checked: Boolean) = when {
    !checked -> GhostBorder
    tone == "bright" -> TerminalGreen
    tone == "easy" -> TerminalGreen
    tone == "tense" -> Warning
    tone == "heavy" -> GhostTextDim
    tone == "mind" -> GhostText
    else -> GhostTextDim
}

/** TODAY'S FORM: the feelings as the four quadrants and the mind row, the box's guesses ticked
 *  before you look and marked "·" (one tap unticks), the why prefilled from the day, a voice note,
 *  the save. The check-in text records the guesses left standing ("Preselected: tired"), so a
 *  later look can tell a guess from a feeling picked. */
@Composable
private fun CheckinForm(today: String, history: List<BoxClient.CheckinRow>, onSaved: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var why by rememberSaveable { mutableStateOf("") }
    var prefilled by remember { mutableStateOf(false) }
    var suggested by remember { mutableStateOf<List<String>>(emptyList()) }
    var preselected by remember { mutableStateOf<List<String>>(emptyList()) }
    var picked by remember { mutableStateOf<List<String>>(emptyList()) }
    var touched by remember { mutableStateOf(false) }
    var saving by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }
    val usual = remember(history) { Feelings.usual(history.take(30).map { it.feelings }).take(6).toSet() }
    val rec by VoiceCapture.state.collectAsState()

    // the prefill: the box's guesses (the first two ticked, unless the person already chose) and a
    // why from the day's numbers; asked once
    LaunchedEffect(Unit) {
        if (prefilled) return@LaunchedEffect
        prefilled = true
        val d = BoxClient.daySummary(ctx) ?: return@LaunchedEffect
        suggested = d.suggested.filter { it in Feelings.all }
        if (!touched) {
            preselected = Feelings.preselect(suggested)
            picked = preselected
        }
        if (why.isEmpty()) {
            val bits = ArrayList<String>()
            if (d.sleepMinutes > 0) bits.add("slept ${d.sleepMinutes / 60}h${"%02d".format(d.sleepMinutes % 60)}m")
            if (d.steps > 0) bits.add("${"%,d".format(d.steps)} steps")
            if (d.exerciseMinutes >= 10) bits.add("${d.exerciseMinutes}m exercise")
            if (d.photos > 0) bits.add("${d.photos} photo${if (d.photos == 1) "" else "s"}" +
                (if (d.places.isNotEmpty()) " around " + d.places.take(2)
                    .joinToString(" and ") { it.substringAfterLast(" / ") } else ""))
            if (d.places.isNotEmpty()) bits.add("was at " +
                d.places.take(3).joinToString("; ") { it.substringAfterLast(" / ") })
            d.notes.filterNot { it.startsWith("Voice note") }.take(2).forEach { bits.add(it) }
            why = if (bits.isEmpty()) "" else "Today: " + bits.joinToString(". ") + "."
        }
    }

    val tap: (String) -> Unit = { f -> touched = true; picked = Feelings.toggle(picked, f) }
    Column(Modifier.fillMaxWidth().animateContentSize()) {
        Text("how are you feeling?", color = GhostText, style = MaterialTheme.typography.titleMedium)
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("${picked.size} of ${Feelings.MAX_PICKS}", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            if (suggested.isNotEmpty()) Text("  · marks the box's guess from your day (sleep, steps, places)", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(10.dp))
        // the four quadrants, two by two; the mind row across
        val quads = Feelings.groups.filter { it.label != "mind" }
        for (pair in quads.chunked(2)) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (g in pair) {
                    Column(Modifier.weight(1f).border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                        Text("${Feelings.mark(g.label)} ${g.label}", color = toneColour(g.label, true), style = MaterialTheme.typography.labelMedium)
                        Text(g.hint, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                        Spacer(Modifier.height(6.dp))
                        FeelingChips(g.feelings, picked, suggested, usual, tap, maxChars = 18, maxPer = 2)
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
        }
        Feelings.groups.firstOrNull { it.label == "mind" }?.let { g ->
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                Text("${Feelings.mark(g.label)} ${g.label} · ${g.hint}", color = toneColour(g.label, true), style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(6.dp))
                FeelingChips(g.feelings, picked, suggested, usual, tap, maxChars = 40, maxPer = 6)
            }
        }
        Spacer(Modifier.height(12.dp))
        Text("why?", color = GhostText, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(4.dp))
        BasicTextField(why, { why = it },
            textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
            decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
                .padding(8.dp)) { if (why.isEmpty()) Text("prefilled from your day once the box answers · edit freely",
                    color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
            modifier = Modifier.fillMaxWidth().heightIn(min = 56.dp))
        Spacer(Modifier.height(12.dp))
        Text("or say it", color = GhostText, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(4.dp))
        // the take in hand goes with the check-in when it is saved
        VoiceRecorder(hint = "a minute about the day, in your own words · kept and transcribed on your box",
            saveLabel = null, onSave = {})
        Spacer(Modifier.height(14.dp))
        val take = rec.take
        val canSave = !saving && !rec.recording && (picked.isNotEmpty() || why.isNotBlank() || take != null)
        Text(when { saving -> "saving…"; rec.recording -> "[ save check-in ] · stop the recording first"; else -> "[ save check-in ]" },
            color = if (canSave) TerminalGreen else TerminalDim, style = MaterialTheme.typography.titleMedium,
            modifier = Modifier.clickable {
                if (!canSave) return@clickable
                saving = true
                note = ""
                scope.launch {
                    val text = Feelings.checkinText(today, picked, preselected, why, take?.id, take?.durationMs ?: 0L)
                    if (BoxClient.noteAdd(ctx, text)) {
                        if (take != null) {
                            VoicePlayback.stop()
                            VoiceNotes.enqueue(ctx, take, "checkin", today)
                            VoiceCapture.taken()
                        }
                        AppSettings.setLastCheckinDay(ctx, today)
                        if (take != null) VoiceNotes.uploadPending(ctx)
                        onSaved()
                    } else {
                        note = "! the box did not answer · nothing is lost (the voice note stays here); try again when it is reachable"
                    }
                    saving = false
                }
            })
        if (note.isNotEmpty()) Text(note, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
    }
}

/** Feeling chips in rows that fit [maxChars]: a picked one is filled green, the box's guess carries
 *  "·", one of your usual ones is brighter than the rest. */
@Composable
private fun FeelingChips(words: List<String>, picked: List<String>, guessed: List<String>, usual: Set<String>,
                         onTap: ((String) -> Unit)?, maxChars: Int, maxPer: Int) {
    for (row in Feelings.rows(words, maxChars, maxPer)) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.padding(bottom = 6.dp)) {
            for (f in row) {
                val on = f in picked
                val label = (if (f in guessed) "·" else "") + f
                Text(label,
                    color = if (on) Void else if (f in usual) GhostText else GhostTextDim,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.border(1.dp, if (on) TerminalGreen else GhostBorder, RectangleShape)
                        .background(if (on) TerminalGreen else Void)
                        .then(if (onTap != null) Modifier.clickable { onTap(f) } else Modifier)
                        .padding(horizontal = 8.dp, vertical = 4.dp))
            }
        }
    }
}

/** TODAY, CHECKED IN: what was said (the feelings, the why, every voice note said to the check-in
 *  as the box has it, and the ones still on the phone), the day as the box tells it, and a
 *  recorder that adds to the check-in until the day ends: a note said here is a check-in note (kind
 *  checkin, today), listed with the first and journaled "said at the daily check-in". [row] is null
 *  while the check-in is still on its way into the journal (noted ingests on its next tick). */
@Composable
private fun CheckedIn(today: String, row: BoxClient.CheckinRow?, onPhone: Set<String>, waiting: List<VoiceNotes.Pending>,
                      justSaved: Boolean, onSaved: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    Column(Modifier.fillMaxWidth().animateContentSize()) {
        val picks = row?.let { Feelings.picks(it.feelings) } ?: emptyList()
        Text("✓ checked in" + (if (picks.isNotEmpty()) " · " + picks.joinToString(", ") else ""),
            color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
        if (row == null) {
            Text("on its way into the journal · here within a minute", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        } else {
            val guessed = Feelings.picks(row.preselected)
            if (picks.isNotEmpty()) {
                Spacer(Modifier.height(6.dp))
                FeelingChips(picks, picks, guessed, emptySet(), onTap = null, maxChars = 40, maxPer = 6)
            }
            if (row.why.isNotBlank()) Text(row.why, color = GhostText, style = MaterialTheme.typography.bodySmall)
        }
        val said = row?.voices ?: emptyList()
        said.forEach { v -> key("today-" + v.id) { VoiceNoteCard(v, onPhone = v.id in onPhone, onDelete = null) } }
        // said to the check-in, not yet on the box (no network, or the upload still going)
        waiting.filter { p -> said.none { it.id == p.id } }.forEach { p ->
            key("today-local-" + p.id) {
                VoiceNoteCard(BoxClient.VoiceNoteRow(p.id, p.kind, p.day, p.takenAt / 1000, p.durationMs, "missing", "", "", ""), onPhone = true, onDelete = null)
            }
        }
        Spacer(Modifier.height(12.dp))
        // YOUR DAY, written up: once the check-in is in, the box folds the photos, the trail, the
        // health sync, the voice notes and what you said into one telling of the day (synthd
        // days.go). Written on request right after the check-in (a minute or two: it waits for the
        // check-in to land, then the model writes); read back after.
        DayStoryCard(today, justSaved = justSaved)
        Spacer(Modifier.height(10.dp))
        VoiceRecorder(hint = "more to say about today · added to the check-in until the day ends, transcribed on your box",
            saveLabel = "[ add to today's check-in ]", onSave = { take ->
                VoiceNotes.enqueue(ctx, take, "checkin", today)
                VoiceCapture.taken()
                scope.launch { VoiceNotes.uploadPending(ctx); onSaved() }
            })
    }
}

/** ONE PAST CHECK-IN: the day, what was felt (the box's guesses marked), the why, the voice note
 *  with its words, the day as the box told it, and the whole day. */
@Composable
private fun PastCheckin(day: String, today: String, row: BoxClient.CheckinRow?, onPhone: Set<String>, onBack: () -> Unit, onOpenDay: () -> Unit) {
    val ctx = LocalContext.current
    var story by remember(day) { mutableStateOf<BoxClient.DayStory?>(null) }
    var storyRead by remember(day) { mutableStateOf(false) }
    LaunchedEffect(day) { story = BoxClient.dayStory(ctx, day); storyRead = true }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Text("‹ check-in", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
        Spacer(Modifier.height(8.dp))
        Text(DayText.heading(day), color = GhostText, style = MaterialTheme.typography.titleLarge)
        Text(DayText.ago(day, today), color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(16.dp))
        if (row == null) {
            Text("no check-in on this day", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        } else {
            val picks = Feelings.picks(row.feelings)
            val guessed = Feelings.picks(row.preselected)
            val tone = Feelings.tone(row.feelings, row.preselected)
            SectionLabel("FELT")
            Spacer(Modifier.height(6.dp))
            if (picks.isEmpty()) {
                Text("no feeling picked", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            } else {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(Feelings.mark(tone), color = toneColour(tone, true), style = MaterialTheme.typography.titleMedium, modifier = Modifier.padding(end = 8.dp))
                    Column { FeelingChips(picks, picks, guessed, emptySet(), onTap = null, maxChars = 36, maxPer = 6) }
                }
                if (guessed.any { it in picks }) Text("· was the box's guess from the day, left standing", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            if (row.why.isNotBlank()) {
                Spacer(Modifier.height(14.dp))
                SectionLabel("WHY")
                Spacer(Modifier.height(6.dp))
                Text(row.why, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            }
            if (row.voices.isNotEmpty()) {
                Spacer(Modifier.height(14.dp))
                SectionLabel(if (row.voices.size == 1) "SAID" else "SAID, ${row.voices.size} NOTES")
                row.voices.forEach { v -> key("past-" + v.id) { VoiceNoteCard(v, onPhone = v.id in onPhone, onDelete = null) } }
            }
        }
        Spacer(Modifier.height(18.dp))
        SectionLabel("THE DAY, AS THE BOX TOLD IT")
        Spacer(Modifier.height(6.dp))
        val st = story
        when {
            !storyRead -> LoadingRow()
            st != null && st.summary.isNotBlank() -> {
                if (st.title.isNotBlank()) Text(st.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
                Spacer(Modifier.height(4.dp))
                Text(st.summary, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            }
            else -> Text("no story of this day · the whole day can write one", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(16.dp))
        Text("[ the whole day › ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onOpenDay() }.padding(vertical = 4.dp))
        Spacer(Modifier.height(24.dp))
    }
}

/** The day, told by the box after the check-in. [justSaved]: ask the box to write it now (the
 *  voice note, if any, is still being transcribed; the story is written again by the nightly pass
 *  once it lands). Otherwise read what is there. */
@Composable
private fun DayStoryCard(day: String, justSaved: Boolean) {
    val ctx = LocalContext.current
    var story by remember { mutableStateOf<BoxClient.DayStory?>(null) }
    var state by remember { mutableStateOf(if (justSaved) "writing" else "reading") }
    var open by remember { mutableStateOf(true) }
    LaunchedEffect(day, justSaved) {
        if (justSaved) {
            // the voice note's upload goes first, so the transcript can be in the telling
            kotlinx.coroutines.delay(2_000)
            val s = BoxClient.dayStory(ctx, day, build = true)
            story = s
            state = if (s == null) "failed" else if (s.summary.isBlank()) "empty" else "ok"
        } else {
            val s = BoxClient.dayStory(ctx, day)
            story = s
            state = if (s == null) "failed" else if (s.summary.isBlank()) "empty" else "ok"
        }
    }
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Text(if (open) "[ − your day, as the box tells it ]" else "[ + your day, as the box tells it ]",
            color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { open = !open })
        if (open) {
            Spacer(Modifier.height(6.dp))
            when (state) {
                "writing" -> Text("writing up your day from the photos, the trail, the health sync, your voice note and what you just said… a minute or two",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                "reading" -> Text("reading…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                "failed" -> Text("! the box did not answer · the day is written by the nightly pass anyway; it will be here tomorrow, and in MEMORIES",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                "empty" -> Text("nothing to tell yet · the day is written once there are photos, a trail or a check-in on the box",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                else -> story?.let { s ->
                    if (s.title.isNotBlank()) Text(s.title, color = GhostText, style = MaterialTheme.typography.titleMedium)
                    Spacer(Modifier.height(4.dp))
                    Text(s.summary, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(4.dp))
                    Text(if (s.writtenBy == "model") "written by the box's model from the day's facts · it is in MEMORIES"
                        else "the day's facts, in the box's plain words · the model writes it up in the evening pass",
                        color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
            }
        }
    }
}

/** THE RECORDER: record, stop, listen, again, drop. [saveLabel] null = the take is saved by the
 *  form's own button (the check-in); else this row saves it. The screen stays on while it records
 *  (a locked phone gives an app silence from the microphone). */
@Composable
internal fun VoiceRecorder(hint: String, saveLabel: String?, onSave: (VoiceCapture.Take) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val rec by VoiceCapture.state.collectAsState()
    val playing by VoicePlayback.playing.collectAsState()
    var denied by remember { mutableStateOf(false) }
    val view = LocalView.current
    DisposableEffect(rec.recording) {
        view.keepScreenOn = rec.recording
        onDispose { view.keepScreenOn = false }
    }
    val mic = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { ok ->
        denied = !ok
        if (ok) VoiceCapture.start(ctx)
    }
    fun record() {
        VoicePlayback.stop()
        if (ContextCompat.checkSelfPermission(ctx, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
            VoiceCapture.start(ctx)
        } else {
            mic.launch(Manifest.permission.RECORD_AUDIO)
        }
    }
    val take = rec.take
    Column(Modifier.fillMaxWidth()) {
        when {
            rec.recording -> Row(verticalAlignment = Alignment.CenterVertically) {
                Text("● " + Feelings.clock(rec.elapsedMs), color = Warning, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.width(8.dp))
                Box(Modifier.width(72.dp).height(6.dp).background(GhostBorder)) {
                    Box(Modifier.fillMaxHeight().fillMaxWidth(rec.level.coerceIn(0.02f, 1f)).background(TerminalGreen))
                }
                Spacer(Modifier.width(10.dp))
                Text("[ ■ stop ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { VoiceCapture.stop() })
            }
            take != null -> {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("🎙 voice note ${Feelings.clock(take.durationMs)}", color = GhostText, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.width(10.dp))
                    Text(if (playing == take.id) "[ ■ ]" else "[ ▶ ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { scope.launch { VoicePlayback.toggle(ctx, take.id) } })
                    Spacer(Modifier.width(10.dp))
                    Text("[ ● again ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { record() })
                    Spacer(Modifier.width(10.dp))
                    Text("[ ✕ ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { VoicePlayback.stop(); VoiceCapture.discard() })
                }
                if (saveLabel != null) {
                    Text(saveLabel, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.padding(top = 4.dp).clickable { VoicePlayback.stop(); onSave(take) })
                } else {
                    Text("saved with the check-in", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
            }
            else -> {
                Text("[ ● record a voice note ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { record() })
                Text(hint, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
        if (rec.error.isNotEmpty()) Text("! " + rec.error, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        if (denied) Text("! no microphone permission · allow it for LocalGhost in the phone's settings to record",
            color = TerminalDim, style = MaterialTheme.typography.labelMedium)
    }
}

/** A note's line under its check-in, or in the notes list: length, then the words or where it is. */
private fun voiceStatus(v: BoxClient.VoiceNoteRow, onPhone: Boolean): String = when (v.status) {
    "done" -> v.transcript.ifBlank { "(nothing the box could hear)" }
    "pending" -> pendingWhy(v.id, BoxClient.voiceQueue)
    "failed" -> "not transcribed: " + v.error.ifBlank { "the speech engine failed" }
    "missing" -> if (onPhone) "on the phone, waiting to reach the box" else "not on the box"
    else -> v.status
}

/** A waiting note's line, with the box's reason when it has one (an older box sends none). */
internal fun pendingWhy(id: String, q: BoxClient.VoiceQueue?): String = when {
    q == null -> "on the box, waiting to be transcribed"
    q.working == id -> "on the box, being transcribed now"
    !q.running -> "on the box, waiting: the box's voice service is not running"
    q.why.isNotBlank() -> "on the box, waiting: " + q.why
    else -> "on the box, in line to be transcribed"
}

/** One voice note: when, how long, what was said (tap to read it all), play, delete. */
@Composable
internal fun VoiceNoteCard(v: BoxClient.VoiceNoteRow, onPhone: Boolean, onDelete: (() -> Unit)?) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val playing by VoicePlayback.playing.collectAsState()
    var full by remember { mutableStateOf(false) }
    var confirmDel by remember { mutableStateOf(false) }
    var failed by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            val whenText = if (v.takenAt > 0) java.text.SimpleDateFormat("EEE d MMM, HH:mm", java.util.Locale.UK)
                .format(java.util.Date(v.takenAt * 1000)) else v.day
            Text("🎙 $whenText · ${Feelings.clock(v.durationMs)}" + (if (v.kind == "checkin") " · check-in" else ""),
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
            if (v.status != "missing" || onPhone) {
                Text(if (playing == v.id) " [ ■ ]" else " [ ▶ ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { scope.launch { failed = !VoicePlayback.toggle(ctx, v.id) } })
            }
            if (onDelete != null) {
                if (confirmDel) {
                    LaunchedEffect(confirmDel) { kotlinx.coroutines.delay(3000); confirmDel = false }
                    Text(" [ delete? ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { VoicePlayback.stop(); onDelete() })
                } else {
                    Text(" 🗑", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { confirmDel = true })
                }
            }
        }
        val words = voiceStatus(v, onPhone)
        Text(words, color = if (v.status == "done") GhostText else TerminalDim, style = MaterialTheme.typography.bodySmall,
            maxLines = if (full) Int.MAX_VALUE else 3,
            modifier = Modifier.clickable { full = !full })
        if (failed) Text("! the audio could not be had from the box", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
    }
}
