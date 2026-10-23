# What else the phone can hand the box

The calendar is built (`hw/calendarstore.go`, `sync/CalendarSync.kt`, docs below). This page is
the plan for the next five: contacts, screen time, the call log, SMS, and notifications (which is
where WhatsApp comes in). Each one is read on the phone, sent over the paired channel, kept on
the box, and never sent anywhere else; each has its own switch in SETTINGS and its own FORGET.

| Source | Android permission | How it is granted | History | What it feeds |
|---|---|---|---|---|
| Contacts | `READ_CONTACTS` | ordinary dialog | everything the phone holds | names for numbers and addresses everywhere, people, birthdays |
| Screen time | `PACKAGE_USAGE_STATS` | special access, Settings › Usage access | events about a week, daily totals longer | the day's story, HEALTH's page, "how long was I on my phone" |
| Call log | `READ_CALL_LOG` | ordinary dialog | what the phone kept (usually months) | the day's story, people, "when did I last speak to Paul" |
| SMS | `READ_SMS` | ordinary dialog | everything the phone kept | the day's story, people, chat questions |
| Notifications, WhatsApp | a `NotificationListenerService` | special access, Settings › Notification access | from the day it is turned on | messages as they arrive, the day's story, chat questions |

Order: **contacts** (done, 16 Oct), then **screen time**, then **notifications** (WhatsApp with
them), then **call log and SMS** together. Before screen time, PERMISSIONS becomes the one list
of everything: the ordinary dialogs under GRANT ALL, the special accesses (usage access,
notification access, the battery) as a walk that opens each settings page in turn.

## The calendar (done, for reference)

`READ_CALENDAR`; the phone's calendar store (`CalendarContract.Instances`), a year back and a
year ahead, every calendar the phone syncs, each instance with its guests and notes. Sent when
the store changes (a WorkManager content trigger), every six hours, and on unlock when the last
read is a quarter of an hour old; skipped when nothing changed. The box replaces the window
whole (`POST /v1/calendar/upload`), so an event deleted on the phone leaves the box. It feeds the
CALENDAR page, NEXT on HOME, the DAY page, the day's story ("planned, not proof it happened"),
and the chat (a question about plans is answered from the box's copy and never goes to the web).
Read-only, everywhere.

## Contacts (built 16 Oct 2026)

**Reads** `ContactsContract.Data`, one person per aggregated contact: display name, given and
family names, nickname, organisation and title, phone numbers (the provider's normalised E.164,
else `PhoneNumberUtils` with the SIM's country; deduplicated by their last nine digits, labelled
mobile, work, home…), email addresses, the birthday (`Event.TYPE_BIRTHDAY`, normalised to
YYYY-MM-DD or --MM-DD), starred. Not the photo, not the notes, not the account. Sent whole when
the provider changes (a content trigger on `Contacts.CONTENT_URI`), every six hours, and on
unlock when the last read is an hour old; hash-skipped when unchanged.

**On the box**: `contacts` (`hw/contactstore.go`), one row per person keyed `device|lookup`,
replaced whole per upload. `ContactFor(number|address)` matches a number in any of its forms by
its last nine digits and an address case-blind; a number two contacts share names nobody unless
one is starred. `ContactSearch`, `ContactBirthdays`, `ContactLine`, `BirthdayLine`.
`POST /v1/contacts/upload`, `GET /v1/contacts?q=|for=`, `POST /v1/contacts/forget`.

**Feeds now**: the CONTACTS page (a card under INTEGRATIONS, like the calendar); the chat ("what's
Ana's number", "Paul's email", "whose birthday is this week", "who is +44 7700 900123", answered
from the box and never from the web); birthdays in the day's sheet and story (a day with nothing
else stays empty). **Next**: the name on a mail sender, a call, an SMS, a WhatsApp sender; the
people merge; "in your contacts" as a reason a mail is not spam.

**Limits**: what the phone's contacts hold and nothing else; a contact only in WhatsApp's own
list is not in the phone's contacts.

## Screen time

**Reads** `UsageStatsManager`. `queryEvents` gives each app coming to the front and leaving it
(`ACTIVITY_RESUMED`, `ACTIVITY_PAUSED`), the screen on and off, and the phone unlocked
(`KEYGUARD_HIDDEN`); from those the phone computes per app per day the minutes in front, how
many times it was opened, and the day's unlocks and first and last use. The events are kept by
Android for about a week, so the read runs daily (the six-hourly worker is enough); older days
come from `queryUsageStats` daily buckets, minutes per app only.

**Permission**: `PACKAGE_USAGE_STATS` is a special access the person turns on in Settings ›
Usage access (the app opens that page); it cannot be asked for with a dialog.

**On the box**: `usage_daily (day, package, label, minutes, opens)` and `usage_day (day, unlocks,
first_use, last_use, screen_minutes)`.

**Feeds**: the day's sheet ("on the phone 3 h 10 min, most of it Chrome and WhatsApp, first
unlocked 07:12"); HEALTH's page beside sleep (the last use before sleep and the first after); the
chat ("how much was I on Instagram this week").

**Limits**: minutes in front, not what was done in the app; per day only in the history.

## Call log

**Reads** `CallLog.Calls`: number, the name the phone cached for it, in or out or missed or
rejected, when, how long. The last read's newest call is the cursor, so each sync sends what is
new; the first sends everything the phone kept.

**On the box**: `calls (key, number, name, direction, at, seconds)`, keyed by number and time.

**Feeds**: the day's sheet ("you spoke to Paul for 25 minutes"); the people pages (when you last
spoke, how often); the chat ("when did I last call mum").

**Limits**: what the phone kept (Android trims the log, often to a few months or a few hundred
calls); WhatsApp and other apps' calls are not in it (a WhatsApp call shows as a notification,
see below).

## SMS

**Reads** `Telephony.Sms` (inbox and sent): address, body, when, read or not. Cursor by the
newest id. MMS text parts can come later.

**On the box**: `sms (key, address, name, direction, at, body)`; searchable like the notes.

**Feeds**: the day's sheet (who you texted, not what, unless the person turns the words on);
the chat ("what was the code the bank sent" is better not answered at all: messages from
short-code senders and anything that looks like a one-time code are kept out on the phone).

**Limits**: RCS chats (Google Messages' "chat" bubbles) are generally not in the SMS store and
not readable by another app; whether a phone's own messages app writes them there varies, so the
first read will say what it found. They arrive through notifications either way. If the app is ever published on Google Play, Play refuses `READ_CALL_LOG` and
`READ_SMS` to an app that is not the default phone or SMS app; installed as now, they work.

## Notifications, and WhatsApp

**Reads** every notification the phone posts, through a `NotificationListenerService` the person
enables in Settings › Notification access. For a messaging app the notification carries
`MessagingStyle`: the conversation's name, and each recent message's sender, text and time. That
is how WhatsApp (and Signal, Telegram, Messenger, Slack, Google Messages' RCS) can be read: on
the phone, after it is decrypted for display, without touching the app or its backup. The phone
keeps a small queue, deduplicates (a notification is reposted with every new message; the key is
app, conversation, sender, time and a hash of the text) and sends it on the usual sync.

**A per-app list**, off by default except the ones the person ticks (WhatsApp, Signal, Messages);
everything else is ignored on the phone and never leaves it. One-time codes and anything from a
bank's or a delivery company's sender are dropped on the phone.

**On the box**: `phone_messages (key, app, conversation, sender, at, text, incoming)`; searchable,
and the people merge takes the senders.

**Feeds**: the day's sheet ("WhatsApp with Paul and the family group"); the people pages; the
chat ("what did Paul say about Thursday"); later, a digest of the groups.

**What it cannot see**, said plainly in the app so nobody mistakes it for the whole history:

- only what arrives while the listener is on; nothing from before;
- muted chats and archived ones post no notification, so none of their messages;
- a chat open on the screen when the message comes posts none;
- your own messages only when the app includes them in the notification (WhatsApp does for the
  recent replies in a conversation, not always);
- a burst can be summarised ("5 new messages") on some phones, edits and deletions are not
  seen, media is "Photo" or "Voice message";
- Android 15 and later hide the content of notifications that carry a one-time code from any
  listener that is not a system one, so those arrive empty (which is what we want anyway).

**WhatsApp's history**: WhatsApp's own "Export chat" (a chat › ⋮ › More › Export chat, without
media) writes the chat as a text file; the app takes it from the share sheet and the box parses
it into the same table, marked as imported. One chat at a time, by the person, and the only
honest way to get the past. The encrypted backup (`msgstore.db.crypt15`) is not proposed: it
needs the 64-digit key and depends on a format WhatsApp changes.

**A word on the other side**: these are messages other people sent you. They stay on your box
like your photos of them do; the chat never quotes one into anything that leaves the box, and
FORGET drops a conversation or the lot.

## Shared mechanics

- One switch and one FORGET per source in SETTINGS; turning a source off cancels its workers and
  the box keeps what it has until FORGET.
- Every upload is a window or a cursor, replaced or appended idempotently, so a retry never
  doubles a row.
- Each source gets an Explain topic, a line in PERMISSIONS, and a line in the day's sheet marked
  for what it is (planned, said, sent).
- Nothing of any of them is ever sent to the web or to a model outside the box.
