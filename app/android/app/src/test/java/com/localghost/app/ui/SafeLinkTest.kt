package com.localghost.app.ui

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SafeLinkTest {
    @Test fun web_addresses_open() {
        for (u in listOf("https://www.bbc.co.uk/news/articles/c1", "http://example.org", "HTTPS://Example.org/a?b=c#d",
            "https://example.org:8443/x", "https://en.wikipedia.org/wiki/Corfu_(city)", " https://example.org/ "))
            assertTrue(u, SafeLink.isWeb(u))
    }

    @Test fun anything_else_does_not() {
        for (u in listOf("", "example.org", "tel:+441234567890", "sms:123?body=hi", "intent://scan/#Intent;scheme=zxing;end",
            "content://com.android.contacts/contacts/1", "file:///sdcard/Download/x.html", "javascript:alert(1)",
            "market://details?id=x", "bankapp://pay?to=x", "httpx://example.org", "https://", "https:///path",
            "https://bank.example@other.example/", "https://exa mple.org/", "https://example.org/\u0000", "https://\\\\host/share",
            "data:text/html,<script>1</script>", "https://" + "a".repeat(3000)))
            assertFalse(u, SafeLink.isWeb(u))
    }
}
