package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId

class MailTextTest {
    private val paul = MailText.Row(1, "in", "new", "paul@example.org", "Paul", listOf("agent@lgs.example", "cristina@example.org"), listOf("x@y.example"),
        "", "the flight times", 1760000000, 1760000100, "Thursday works.", 1, "low")
    private val sent = MailText.Row(2, "out", "bounced", "agent@lgs.example", "", listOf("paul@example.org"), emptyList(), "", "Re: the flight times",
        1760000200, 1760000200, "", 0, "", lastErr = "paul@example.org: 550 no such user")

    @Test fun theListLines() {
        assertEquals("Paul", MailText.who(paul))
        assertEquals("paul@example.org", MailText.who(paul.copy(fromName = "")))
        assertEquals("to paul@example.org", MailText.who(sent))
        assertEquals("Thursday works.", MailText.second(paul))
        assertEquals("not delivered , paul@example.org: 550 no such user", MailText.second(sent))
        assertEquals("waiting at the relay", MailText.second(sent.copy(state = "queued")))
        assertEquals("new · 1 file", MailText.marks(paul))
        assertEquals("2 files · spam?", MailText.marks(paul.copy(state = "seen", attachments = 2, spam = "high")))
        assertEquals("(no subject)", MailText.subject(""))
    }

    @Test fun theTimes() {
        val z = ZoneId.of("Europe/London")
        val now = 1760000000L // Thursday 9 October 2025, 09:53 BST
        assertEquals("09:53", MailText.whenShort(now, now, z))
        assertEquals("Tue 09:53", MailText.whenShort(now - 2 * 86400, now, z))
        assertEquals("9 Sep", MailText.whenShort(now - 30 * 86400, now, z))
        assertEquals("9 Oct 2024", MailText.whenShort(now - 365 * 86400, now, z))
        assertEquals("", MailText.whenShort(0, now, z))
        assertTrue(MailText.whenLong(now, z).startsWith("Thursday 9 October 2025, 09:53"))
    }

    @Test fun theVerdicts() {
        val v = MailText.Verdicts("pass", "example.org", "pass", "pass", "reject", "dkim+spf", "ok", true, emptyList(), "low", emptyList())
        val lines = MailText.verdictLines(v)
        assertEquals(4, lines.size)
        assertEquals("SPF pass: the sending server may send for the sender's domain", lines[0])
        assertEquals("DKIM pass: signed by example.org, unchanged since", lines[1])
        assertEquals("DMARC: aligned with the From address (dkim+spf)", lines[2])
        assertEquals("reverse DNS ok, matches the server's name", lines[3])
        assertEquals("looks genuine", MailText.spamLine(v))
        val bad = MailText.Verdicts("fail", "", "none", "fail", "reject", "", "none", false, listOf("the display name pretends to be this box"), "high", listOf("SPF fail", "no DKIM signature"))
        val bl = MailText.verdictLines(bad)
        assertEquals("DMARC: NOT aligned with the From address, and the domain asks for reject", bl[2])
        assertEquals("headers: the display name pretends to be this box", bl[4])
        assertEquals("likely spam · SPF fail, no DKIM signature", MailText.spamLine(bad))
        val off = MailText.Verdicts("", "", "", "", "", "", "", false, emptyList(), "", emptyList())
        assertEquals("SPF not checked", MailText.verdictLines(off)[0])
    }

    @Test fun whoAReplyGoesTo() {
        val (to, cc) = MailText.replyTo(paul, "agent@lgs.example", all = false)
        assertEquals(listOf("paul@example.org"), to)
        assertTrue(cc.isEmpty())
        val (to2, cc2) = MailText.replyTo(paul, "agent@lgs.example", all = true)
        assertEquals(listOf("paul@example.org"), to2)
        assertEquals(listOf("cristina@example.org", "x@y.example"), cc2)
        val (to3, _) = MailText.replyTo(paul.copy(replyTo = "list@example.org"), "agent@lgs.example", all = false)
        assertEquals(listOf("list@example.org"), to3)
        val (to4, _) = MailText.replyTo(sent, "agent@lgs.example", all = false)
        assertEquals(listOf("paul@example.org"), to4)
        assertEquals("Re: x", MailText.replySubject("x"))
        assertEquals("Re: x", MailText.replySubject("Re: x"))
        assertEquals("Fwd: x", MailText.forwardSubject("x"))
        assertEquals("FW: x", MailText.forwardSubject("FW: x"))
    }

    @Test fun theSmallLines() {
        assertEquals("tickets.pdf · 12 KB · application/pdf", MailText.attachmentLine(MailText.Attachment(1, "tickets.pdf", "application/pdf", 12288, true)))
        assertEquals("setup.exe (an executable is not kept) · not kept", MailText.attachmentLine(MailText.Attachment(2, "setup.exe (an executable is not kept)", "", 0, false)))
        assertEquals("1.5 MB", MailText.bytes(1572864))
        assertEquals("3 messages · 2 new · 1 sent · 1 waiting at the relay", MailText.counts(3, 2, 1, 1))
        assertEquals("1 message", MailText.counts(1, 0, 0, 0))
        assertEquals("no mail yet", MailText.empty("inbox", ""))
        assertEquals("nothing matches \"paul\"", MailText.empty("inbox", "paul"))
        assertEquals("not set up", MailText.settingsLine("", "", false, ""))
        assertEquals("agent@lgs.example · the relay is not set", MailText.settingsLine("agent@lgs.example", "", false, ""))
        assertEquals("agent@lgs.example through mx.localghost.ai", MailText.settingsLine("agent@lgs.example", "mx.localghost.ai", true, ""))
        assertEquals("pulled just now", MailText.statusLine(true, 100, 100, "", 0, 110))
        assertEquals("pulled 5 min ago · 2 still on the relay", MailText.statusLine(true, 100, 100, "", 2, 400))
        assertEquals("no pull has worked yet , relay unreachable", MailText.statusLine(true, 100, 0, "relay unreachable", 0, 400))
        assertTrue(MailText.statusLine(false, 0, 0, "", 0, 0).startsWith("set the domain"))
    }
}
