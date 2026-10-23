package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test
import java.time.LocalDate

class ContactsTextTest {
    private val ana = ContactsText.Contact(1, "l1", "Ana Pop", "Ana", "Pop", "", "Acme", "CTO", true,
        listOf(ContactsText.Number("07700 900123", "+447700900123", "mobile")), listOf(ContactsText.Email("ana@example.org", "home")), "1986-03-14")
    private val bare = ContactsText.Contact(2, "l2", "", "", "", "", "", "", false, listOf(ContactsText.Number("020 7946 0000", "", "")), emptyList(), "")

    @Test fun providerNumbers() {
        assertEquals("mobile", ContactsText.phoneLabel(2, null))
        assertEquals("boat", ContactsText.phoneLabel(0, " Boat "))
        assertEquals("", ContactsText.phoneLabel(99, null))
        assertEquals("work", ContactsText.emailLabel(2, null))
    }

    @Test fun birthdays() {
        val cases = mapOf("1986-03-14" to "1986-03-14", "--03-14" to "--03-14", "--0314" to "--03-14", "19860314" to "1986-03-14",
            "14/03/1986" to "1986-03-14", "14.03.1986" to "1986-03-14", "--02-29" to "--02-29", "1985-02-29" to "", "nonsense" to "", "" to "")
        cases.forEach { (i, want) -> assertEquals(i, want, ContactsText.birthday(i)) }
        assertEquals("14 March 1986 · 40", ContactsText.birthdayLine("1986-03-14", LocalDate.of(2026, 10, 16)))
        assertEquals("14 March 1986 · 39", ContactsText.birthdayLine("1986-03-14", LocalDate.of(2026, 3, 13)))
        assertEquals("14 March", ContactsText.birthdayLine("--03-14", LocalDate.of(2026, 10, 16)))
    }

    @Test fun uploadRoundTrip() {
        val body = ContactsText.upload("phone", listOf(bare, ana))
        val page = ContactsText.parsePage(body)
        assertEquals(listOf(ana, bare), page.contacts) // sorted by lookup key
        assertEquals(ContactsText.fingerprint(body), ContactsText.fingerprint(ContactsText.upload("phone", listOf(ana, bare))))
        assertNotEquals(ContactsText.fingerprint(body), ContactsText.fingerprint(ContactsText.upload("phone", listOf(ana.copy(name = "Ana P"), bare))))
    }

    @Test fun lines() {
        assertEquals("CTO, Acme", ContactsText.second(ana))
        assertEquals("020 7946 0000", ContactsText.name(bare))
        assertEquals("#", ContactsText.letter(bare))
        assertEquals("A", ContactsText.letter(ana))
        assertEquals("the box has no contacts from this phone yet", ContactsText.synced(null, 0))
        assertEquals("synced 2 h ago · 412 contacts, 37 with a birthday", ContactsText.synced(ContactsText.Meta(0, 412, 0, 500, 300, 37), 7300))
    }
}
