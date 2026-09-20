package com.localghost.app.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import com.localghost.app.notify.Notifications
import com.localghost.app.phrases.*
import com.localghost.app.ui.theme.*
import java.util.Calendar

/**
 * PHRASES , ghost.phrased. The phrase you are likely to need, in the language around you, at this
 * hour: good morning and one coffee at eight, the bill at eleven at night. The card at the top is
 * what the lock screen and the widget show; everything under it is the long way round , the whole
 * slot in order, the phrasebook by situation, a drill, the emergency numbers, and the switches.
 *
 * Ethos in one line: no location permission, no network for any of this, packs on the phone. The
 * country is the mobile network's own country code, the same fact the status bar already shows.
 */
@Composable
fun PhrasesScreen() {
    val ctx = LocalContext.current
    var tick by remember { mutableStateOf(0) } // bump to re-resolve after a setting changes
    var preview by remember { mutableStateOf("") } // learn-before-you-fly country, "" = here
    val now = remember(tick, preview) { PhraseNow.resolve(ctx, preview) }
    var slotView by remember(now.slot, preview) { mutableStateOf(now.slot) }
    val pack = now.pack
    val list = remember(pack, slotView) { if (pack == null) emptyList() else PhraseEngine.order(pack, slotView) }
    // The card follows the lock screen's cursor while you look at the live slot; browsing another
    // slot or tapping a row moves a screen-only index, so the lock screen keeps its own place.
    var index by remember(list, now.pick?.index) { mutableStateOf(if (slotView == now.slot) (now.pick?.index ?: 0) else 0) }
    var picker by remember { mutableStateOf<String?>(null) } // "country" | "preview" | null
    var show by remember { mutableStateOf(false) }
    val form = now.form
    val current = list.getOrNull(index)

    fun say(p: Phrase, slow: Boolean = false) {
        if (pack != null) PhraseSpeaker.say(ctx, p.localFor(form), pack.ttsTag, slow)
    }
    fun changed() { tick++; PhraseSurface.refresh(ctx) }

    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(6.dp))
            // WHERE, and how we know. The source is printed because it is the whole privacy story.
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(if (now.where.country.isEmpty()) "○" else "●", color = if (now.where.country.isEmpty()) GhostTextDim else TerminalGreen,
                    style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.width(8.dp))
                Column(Modifier.weight(1f)) {
                    Text(
                        when {
                            preview.isNotEmpty() -> "${CountryNames.of(preview)} · previewing"
                            now.where.source == "practice" -> "practice · no pack for where you are, so ${pack?.name ?: "a language"} for now"
                            now.where.country.isEmpty() -> "no country yet"
                            else -> "${CountryNames.of(now.where.country)} · via ${now.where.source}"
                        },
                        color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Text(
                        if (pack == null) "no phrase pack for this place yet"
                        else "${pack.name} · ${pack.nativeName}" + (if (now.late) " · late hours" else ""),
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                Text(if (preview.isNotEmpty()) "[ back here ]" else "[ change ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { if (preview.isNotEmpty()) preview = "" else picker = "country" })
            }
            if (now.alternatives.size > 1) {
                Spacer(Modifier.height(6.dp))
                Chips(now.alternatives.map { it.nativeName }, now.alternatives.indexOf(pack)) { i ->
                    PhraseState.setLangOverride(ctx, now.alternatives[i].lang); changed()
                }
            }
        }

        item {
            // THE CARD. What the lock screen shows, at full size.
            Column(Modifier.fillMaxWidth().border(1.dp, if (current != null) TerminalGreen else GhostBorder, RectangleShape)
                .background(Void).padding(18.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("${slotView.glyph} ${slotView.label}", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.weight(1f))
                    if (current != null) Text("L${current.level} ${Levels.name(current.level)} · ", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    if (list.isNotEmpty()) Text("${index + 1}/${list.size}", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
                Spacer(Modifier.height(14.dp))
                if (current == null || pack == null) {
                    Text(
                        if (now.where.country.isEmpty())
                            "I do not know where we are. Your phone has no mobile network country right now (Wi-Fi only, or airplane mode). Choose the country by hand."
                        else "No phrases for ${CountryNames.of(now.where.country)} yet. Choose a language to use in the meantime, or teach me: the packs are plain JSON.",
                        color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(12.dp))
                    GhostButton("choose a place", onClick = { picker = "country" })
                } else {
                    Text(current.localFor(form), color = GhostText, style = glow(MaterialTheme.typography.headlineMedium))
                    if (current.roman.isNotEmpty()) {
                        Spacer(Modifier.height(4.dp))
                        Text(current.roman, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    }
                    Spacer(Modifier.height(10.dp))
                    Text(current.sayItFor(form), color = TerminalGreen, style = MaterialTheme.typography.bodyLarge)
                    Spacer(Modifier.height(4.dp))
                    Text(current.en, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    if (current.note.isNotEmpty()) {
                        Spacer(Modifier.height(10.dp))
                        Text("· ${current.note}", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    }
                    Spacer(Modifier.height(16.dp))
                    Row(Modifier.horizontalScroll(rememberScrollState())) {
                        Act("say") { say(current) }
                        Act("slow") { say(current, slow = true) }
                        Act("next") { index = (index + 1) % list.size }
                        Act("show") { show = true }
                        val knownNow = current.id in now.known
                        Act(if (knownNow) "✓ known" else "got it") {
                            PhraseState.setKnown(ctx, pack.lang, current.id, !knownNow); changed()
                        }
                    }
                    Spacer(Modifier.height(12.dp))
                    // the slot's progress, as dots , the same walk the lock screen makes
                    Row {
                        list.forEachIndexed { i, _ ->
                            Text(if (i == index) "●" else "·", color = if (i == index) TerminalGreen else TerminalDim,
                                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(end = 2.dp))
                        }
                    }
                    if (!PhraseSpeaker.hasVoice(pack.ttsTag)) {
                        Spacer(Modifier.height(8.dp))
                        Text("no ${pack.name} voice installed , add it under system settings › text-to-speech",
                            color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                    }
                }
            }
        }

        if (pack != null) {
            item {
                SectionLabel("THE DAY")
                Spacer(Modifier.height(6.dp))
                Chips(Slot.entries.map { it.glyph + " " + it.label.lowercase() }, Slot.entries.indexOf(slotView)) { i ->
                    slotView = Slot.entries[i]
                }
                Spacer(Modifier.height(4.dp))
                Text(
                    if (slotView == now.slot) "now · greeting first, then what you are most likely to need"
                    else "browsing · the lock screen stays on ${now.slot.label.lowercase()}",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            items(list.size) { i ->
                val p = list[i]
                PhraseRow(p, form, selected = i == index, known = p.id in now.known,
                    onSelect = { index = i }, onSay = { say(p) },
                    onKnown = { PhraseState.setKnown(ctx, pack.lang, p.id, p.id !in now.known); changed() })
            }

            item { Spacer(Modifier.height(8.dp)); LevelsSection(pack, now.known, now.band, onChange = ::changed) }
            item { Spacer(Modifier.height(8.dp)); Phrasebook(pack, form, now.known, ::say, onChange = ::changed) }
            item { Spacer(Modifier.height(8.dp)); Drill(pack, form, ::say) }
            item { Spacer(Modifier.height(8.dp)); Emergency(pack, form, ::say) }
        }

        item {
            Spacer(Modifier.height(8.dp))
            SectionLabel("LOCK SCREEN & WIDGET")
            Spacer(Modifier.height(6.dp))
            var lock by remember(tick) { mutableStateOf(PhraseState.lockScreenOn(ctx)) }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("card on the lock screen", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Text("a silent notification that changes with the hour · say, next and got it work without unlocking · pull it open for the next two phrases",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                Switch(checked = lock, onCheckedChange = { on ->
                    lock = on; PhraseState.setLockScreenOn(ctx, on); changed()
                }, colors = SwitchDefaults.colors(checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                    uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter, uncheckedBorderColor = GhostBorder))
            }
            if (lock && !Notifications.hasPermission(ctx)) {
                Spacer(Modifier.height(6.dp))
                Text("notifications are off for LocalGhost , the card cannot show until they are allowed",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(4.dp))
                GhostButton("open notification settings", onClick = {
                    ctx.startActivity(Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                        .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, ctx.packageName)
                        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                })
            }
            // LIVE UPDATE (Android 16+): the same card, promoted , the top of the lock screen, the
            // status-bar chip, the always-on display, Samsung's Now Bar. The OS asks the person
            // per app; until they allow it the card stays an ordinary silent notification.
            val canPromote = remember(tick) { PhraseSurface.canPromote(ctx) }
            if (canPromote != null) {
                Spacer(Modifier.height(12.dp))
                var live by remember(tick) { mutableStateOf(PhraseState.liveUpdate(ctx)) }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("live update", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                        Text(if (canPromote) "the card at the top of the lock screen, in the status bar and on the always-on display (Now Bar on Galaxy)"
                            else "allowed for LocalGhost in Settings › Notifications › Live updates, then the card moves to the top of the lock screen and into the Now Bar",
                            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    }
                    Switch(checked = live, onCheckedChange = { on ->
                        live = on; PhraseState.setLiveUpdate(ctx, on); changed()
                    }, colors = SwitchDefaults.colors(checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                        uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter, uncheckedBorderColor = GhostBorder))
                }
                if (live && !canPromote) {
                    Spacer(Modifier.height(4.dp))
                    GhostButton("allow live updates", onClick = {
                        PhraseSurface.promotedSettingsIntent(ctx)?.let { ctx.startActivity(it.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
                    })
                }
            }
            Spacer(Modifier.height(12.dp))
            var pinNote by remember { mutableStateOf("") }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("widget", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Text("home screen anywhere · on the lock screen too: Pixel (Android 16 QPR2+) long-press the lock screen › customize › widgets; Galaxy (One UI 8+) Settings › Lock screen › Widgets",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                GhostButton("add", onClick = {
                    pinNote = if (PhraseSurface.requestPin(ctx)) "" else "your launcher does not offer pinning: long-press the home screen › widgets › LocalGhost"
                })
            }
            if (pinNote.isNotEmpty()) Text(pinNote, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

            if (pack?.genderedSpeech == true || form != SpeakerForm.NEUTRAL) {
                Spacer(Modifier.height(12.dp))
                Text("how you speak", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                Text("some phrases change with the speaker (obrigado / obrigada, khrap / kha) · two people, one phone, two settings",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(6.dp))
                Chips(SpeakerForm.entries.map { it.label }, SpeakerForm.entries.indexOf(form)) { i ->
                    PhraseState.setSpeakerForm(ctx, SpeakerForm.entries[i]); changed()
                }
            }

            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("learn before you fly", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Text("preview another country here · the lock screen keeps following the network",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                GhostButton(if (preview.isEmpty()) "pick" else "stop", onClick = { if (preview.isEmpty()) picker = "preview" else preview = "" })
            }
        }

        item {
            Spacer(Modifier.height(8.dp))
            SectionLabel("ETHOS")
            Spacer(Modifier.height(6.dp))
            val packs = PhrasePacks.all(ctx)
            Text(
                "No location permission: the country is the mobile network's country code, which your phone shows in the status bar anyway. " +
                "Nothing here talks to a network; the packs live in the app. Speech is your phone's own text-to-speech.\n\n" +
                "${packs.size} languages, ${packs.sumOf { it.phrases.size }} phrases, written by a machine and reviewed by nobody yet , " +
                "if one makes a waiter laugh, tell me which. When your box is connected, ghost.phrased will learn what you keep needing; today it does not, and says so.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(24.dp))
        }
    }

    if (picker != null) {
        CountryPicker(
            title = if (picker == "preview") "LEARN BEFORE YOU FLY" else "WHERE ARE WE",
            allowAuto = picker == "country",
            onPick = { code ->
                if (picker == "preview") preview = code
                else { PhraseState.setCountryOverride(ctx, code); PhraseState.setLangOverride(ctx, ""); PhraseState.resetManualNext(ctx); changed() }
                picker = null
            },
            onDismiss = { picker = null },
        )
    }
    if (show && current != null && pack != null) {
        ShowMode(current, form, onSay = { say(current, slow = true) }, onDismiss = { show = false })
    }
}

/** The tap targets of the house style: [ verb ]. */
@Composable
private fun Act(label: String, onClick: () -> Unit) {
    Text("[ $label ]", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium,
        modifier = Modifier.clickable { onClick() }.padding(end = 14.dp, top = 4.dp, bottom = 4.dp))
}

@Composable
private fun Chips(labels: List<String>, selected: Int, onSelect: (Int) -> Unit) {
    Row(Modifier.horizontalScroll(rememberScrollState())) {
        labels.forEachIndexed { i, l ->
            val on = i == selected
            Text(l, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.padding(end = 8.dp)
                    .border(1.dp, TerminalGreen, RectangleShape)
                    .background(if (on) TerminalGreen else Void)
                    .clickable { onSelect(i) }
                    .padding(horizontal = 10.dp, vertical = 6.dp))
        }
    }
}

/** One phrase in a list. The ✓ at the end is the known toggle: lit when the person has it, and a
 *  tap flips it , the quickest way to tell the walk "I have these, show me the rest". */
@Composable
private fun PhraseRow(p: Phrase, form: SpeakerForm, selected: Boolean, known: Boolean = false,
                      onSelect: () -> Unit, onSay: () -> Unit, onKnown: (() -> Unit)? = null) {
    Row(Modifier.fillMaxWidth().clickable { onSelect() }.padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(if (selected) "›" else " ", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(14.dp))
        Column(Modifier.weight(1f)) {
            Text(p.localFor(form), color = if (selected) TerminalGreen else if (known) GhostTextDim else GhostText, style = MaterialTheme.typography.bodyLarge)
            Text("${p.sayItFor(form)}  ·  ${p.en}" + (if (p.level > 1) "  ·  L${p.level}" else ""), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Text("[ say ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onSay() }.padding(start = 8.dp, top = 6.dp, bottom = 6.dp))
        if (onKnown != null) {
            Text(if (known) "✓" else "○", color = if (known) TerminalGreen else TerminalDim, style = MaterialTheme.typography.bodyLarge,
                modifier = Modifier.clickable { onKnown() }.padding(start = 10.dp, end = 2.dp, top = 4.dp, bottom = 4.dp))
        }
    }
}

/**
 * LEVELS: where you are in the language, and the shortcut for someone who already speaks some of
 * it. Each level is a line , known of total , and "I know these" marks the whole level so the
 * walk moves on to the next one at once, instead of waiting for GOT IT taps on forty cards.
 */
@Composable
private fun LevelsSection(pack: PhrasePack, known: Set<String>, band: Int, onChange: () -> Unit) {
    val ctx = LocalContext.current
    val prog = remember(pack.lang, known) { PhraseEngine.progress(pack, known) }
    if (prog.size <= 1 && pack.phrases.none { it.level > 1 }) {
        // A pack without levels: one progress line is enough, and "I know these" still helps.
        val (k, t) = prog[1] ?: (0 to 0)
        Column {
            SectionLabel("KNOWN")
            Spacer(Modifier.height(6.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("$k of $t phrases · known cards leave the walk and come back as reviews", color = GhostTextDim,
                    style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
            }
        }
        return
    }
    Column {
        SectionLabel("LEVELS")
        Spacer(Modifier.height(6.dp))
        Text("the walk shows level $band, ${Levels.name(band)} · a level opens once ${(Levels.DONE_SHARE * 100).toInt()}% of the one below is known · known cards come back as reviews, one in five",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(6.dp))
        for ((level, kt) in prog) {
            val (k, t) = kt
            val ids = pack.phrases.filter { it.level == level && it.situation != Situation.EMERGENCY }.map { it.id }
            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(if (level == band) "›" else " ", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(14.dp))
                Column(Modifier.weight(1f)) {
                    Text("$level · ${Levels.name(level)}", color = if (level <= band) GhostText else GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    Text("$k of $t known", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                if (k < t) GhostButton("i know these", onClick = { PhraseState.setKnownAll(ctx, pack.lang, ids, true); onChange() })
                else GhostButton("start over", onClick = { PhraseState.setKnownAll(ctx, pack.lang, ids, false); onChange() })
            }
        }
    }
}

@Composable
private fun Phrasebook(pack: PhrasePack, form: SpeakerForm, known: Set<String>, say: (Phrase, Boolean) -> Unit, onChange: () -> Unit) {
    val ctx = LocalContext.current
    var open by remember(pack.lang) { mutableStateOf(false) }
    var chapter by remember(pack.lang) { mutableStateOf(Situation.GREETINGS) }
    val chapters = Situation.entries.filter { s -> s != Situation.EMERGENCY && pack.phrases.any { it.situation == s } }
    Column {
        Row(Modifier.fillMaxWidth().clickable { open = !open }, verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("PHRASEBOOK")
            Spacer(Modifier.weight(1f))
            Text(if (open) "▴" else "▾", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        if (!open) {
            Text("${pack.phrases.size} phrases by situation · tap to open", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            return
        }
        Spacer(Modifier.height(6.dp))
        Chips(chapters.map { it.label }, chapters.indexOf(chapter)) { chapter = chapters[it] }
        Spacer(Modifier.height(4.dp))
        pack.phrases.filter { it.situation == chapter }.forEach { p ->
            PhraseRow(p, form, selected = false, known = p.id in known, onSelect = { say(p, false) }, onSay = { say(p, false) },
                onKnown = { PhraseState.setKnown(ctx, pack.lang, p.id, p.id !in known); onChange() })
        }
    }
}

/**
 * Flashcards, the one learning tool that survives a holiday: English up, tap to turn, GOT IT or
 * AGAIN. A phrase that is got three times running stops coming up; AGAIN sends it to the front.
 * Scores live in prefs per phrase, so a week of breakfasts adds up.
 */
@Composable
private fun Drill(pack: PhrasePack, form: SpeakerForm, say: (Phrase, Boolean) -> Unit) {
    val ctx = LocalContext.current
    var open by remember(pack.lang) { mutableStateOf(false) }
    var round by remember(pack.lang) { mutableStateOf(0) }
    val due = remember(pack.lang, round) {
        pack.phrases.filter { it.situation != Situation.EMERGENCY && PhraseState.drillScore(ctx, pack.lang, it.id) < PhraseState.KNOWN_AT }
            .sortedBy { PhraseState.drillScore(ctx, pack.lang, it.id) }
    }
    val solid = pack.phrases.count { it.situation != Situation.EMERGENCY && PhraseState.drillScore(ctx, pack.lang, it.id) >= PhraseState.KNOWN_AT }
    var flipped by remember(pack.lang, round) { mutableStateOf(false) }
    Column {
        Row(Modifier.fillMaxWidth().clickable { open = !open }, verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("DRILL")
            Spacer(Modifier.weight(1f))
            Text(if (open) "▴" else "▾", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        Text("$solid of ${pack.phrases.count { it.situation != Situation.EMERGENCY }} solid · three in a row makes a phrase known, and known phrases leave the lock-screen walk",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        if (!open) return
        Spacer(Modifier.height(8.dp))
        val card = due.firstOrNull()
        if (card == null) {
            Text("everything solid. order the bill in style.", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
            GhostButton("start over", onClick = {
                PhraseState.setKnownAll(ctx, pack.lang, pack.phrases.map { it.id }, false); round++; PhraseSurface.refresh(ctx)
            })
            return
        }
        Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).clickable { flipped = !flipped }.padding(16.dp)) {
            Text(card.en, color = GhostText, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(8.dp))
            if (flipped) {
                Text(card.localFor(form), color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                Text(card.sayItFor(form), color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            } else {
                Text("tap to turn", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        Spacer(Modifier.height(8.dp))
        Row {
            Act("say") { say(card, false) }
            if (flipped) {
                Act("got it") {
                    val n = PhraseState.drillScore(ctx, pack.lang, card.id) + 1
                    PhraseState.setDrillScore(ctx, pack.lang, card.id, n); round++
                    if (n >= PhraseState.KNOWN_AT) Thread { PhraseSurface.refresh(ctx) }.start() // it just left the walk
                }
                Act("again") { PhraseState.setDrillScore(ctx, pack.lang, card.id, 0); round++ }
            }
        }
    }
}

@Composable
private fun Emergency(pack: PhrasePack, form: SpeakerForm, say: (Phrase, Boolean) -> Unit) {
    val ctx = LocalContext.current
    var open by remember(pack.lang) { mutableStateOf(false) }
    val phrases = pack.phrases.filter { it.situation == Situation.EMERGENCY }
    Column {
        Row(Modifier.fillMaxWidth().clickable { open = !open }, verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("EMERGENCY")
            Spacer(Modifier.weight(1f))
            Text(if (open) "▴" else "▾", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        val nums = pack.emergency
        Text(if (nums.isEmpty()) "no numbers listed for this language's countries , 112 works across the EU"
            else nums.joinToString("  ·  ") { "${it.label} ${it.number}" },
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        if (!open) return
        Spacer(Modifier.height(6.dp))
        nums.forEach { n ->
            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("${n.label}  ${n.number}", color = GhostText, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                Text("[ dial ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable {
                        // ACTION_DIAL opens the dialler with the number filled in and does NOT place
                        // the call , no CALL_PHONE permission, and no accidental call from a pocket.
                        ctx.startActivity(Intent(Intent.ACTION_DIAL, Uri.parse("tel:" + n.number)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                    }.padding(6.dp))
            }
        }
        phrases.forEach { p -> PhraseRow(p, form, selected = false, onSelect = { say(p, false) }, onSay = { say(p, false) }) }
    }
}

/** Full screen, full brightness, the phrase alone: turn the phone to the person you are talking to. */
@Composable
private fun ShowMode(p: Phrase, form: SpeakerForm, onSay: () -> Unit, onDismiss: () -> Unit) {
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        val window = (LocalView.current.parent as? DialogWindowProvider)?.window
        DisposableEffect(window) {
            window?.let { w -> w.attributes = w.attributes.apply { screenBrightness = 1f } }
            onDispose { window?.let { w -> w.attributes = w.attributes.apply { screenBrightness = -1f } } }
        }
        Column(Modifier.fillMaxSize().background(Color.Black).clickable { onDismiss() }.padding(28.dp),
            verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            Text(p.localFor(form), color = Color.White, textAlign = TextAlign.Center,
                style = MaterialTheme.typography.displayLarge.copy(fontSize = 44.sp, lineHeight = 54.sp))
            if (p.roman.isNotEmpty()) {
                Spacer(Modifier.height(12.dp))
                Text(p.roman, color = GhostTextDim, textAlign = TextAlign.Center, style = MaterialTheme.typography.titleMedium)
            }
            Spacer(Modifier.height(24.dp))
            Text(p.en, color = GhostTextDim, textAlign = TextAlign.Center, style = MaterialTheme.typography.bodyLarge)
            Spacer(Modifier.height(40.dp))
            Row { Act("say it slowly") { onSay() }; Act("close") { onDismiss() } }
        }
    }
}

@Composable
private fun CountryPicker(title: String, allowAuto: Boolean, onPick: (String) -> Unit, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    val countries = remember { PhrasePacks.countries(ctx).toList().sortedBy { CountryNames.of(it.first) } }
    Dialog(onDismissRequest = onDismiss) {
        Column(Modifier.fillMaxWidth().heightIn(max = 560.dp).border(1.dp, TerminalGreen, RectangleShape).background(Void).padding(18.dp)) {
            Text("> $title", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(10.dp))
            Column(Modifier.verticalScroll(rememberScrollState())) {
                if (allowAuto) {
                    Text("automatic · follow the mobile network", color = GhostText, style = MaterialTheme.typography.bodyMedium,
                        modifier = Modifier.fillMaxWidth().clickable { onPick("") }.padding(vertical = 8.dp))
                }
                countries.forEach { (code, packs) ->
                    Row(Modifier.fillMaxWidth().clickable { onPick(code) }.padding(vertical = 8.dp)) {
                        Text(CountryNames.of(code), color = GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
                        Text(packs.joinToString(" / ") { it.nativeName }, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
            Text("[ close ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onDismiss() })
        }
    }
}

/** For the drawer and the widget note: what would show right now, in one line. */
fun phraseHeadline(ctx: android.content.Context): String {
    val now = PhraseNow.resolve(ctx, "", Calendar.getInstance())
    val p = now.phrase ?: return now.headline.lowercase()
    return "${now.headline.lowercase()} · ${p.localFor(now.form)}"
}
