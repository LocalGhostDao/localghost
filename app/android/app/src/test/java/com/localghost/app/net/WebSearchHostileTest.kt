package com.localghost.app.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A page the search brings back is a stranger's, and the phone reads it on its own thread with
 * nothing to stop it. Until the second audit (12 Nov 2026) the reading was regular expressions
 * that started again at every unclosed opening: a page of four hundred kilobytes of "<nav>" took
 * minutes of one core. These hold the reading to one pass, and to the same answers on an
 * ordinary page.
 */
class WebSearchHostileTest {
    private val cap = 400 * 1024

    private fun fill(unit: String): String = buildString(cap + unit.length) { while (length < cap) append(unit) }

    /** Runs [f] and fails when it takes longer than a page ever should (seconds, not minutes). */
    private fun <T> quick(what: String, f: () -> T): T {
        val t0 = System.nanoTime()
        val v = f()
        val ms = (System.nanoTime() - t0) / 1_000_000
        assertTrue("$what took $ms ms", ms < 5_000)
        return v
    }

    @Test fun pagesBuiltToBeSlowAreReadInOnePass() {
        val prose = "<p>The harbour at Gaios is on the east side of the island, sheltered by two islets, and ferries leave from the new port.</p>"
        val shapes = listOf(
            "<nav>", "<nav >x", "<script>", "<style>a", "<!--", "<!-- -", "<a href=x>", "<a ", "<article>", "<main>",
            "<meta name=a content=b ", "<meta ", "<time ", "<time datetime=", "<title>", "<p", "</p", "<", "<>", "< ",
            "<nav></nav ", "<a></a ", "\"datePublished\" ", "\"datePublished\":\"", "&", "&#", "&#x7fffffff;", "&a",
            "<figure><form><aside><footer><header><template><iframe><svg><noscript>",
        )
        for (shape in shapes) {
            for (page in listOf(fill(shape), "<html><head>" + fill(shape) + "</head><body>" + prose + "</body></html>",
                "<html><body>" + prose + fill(shape) + "</body></html>")) {
                quick("extract of a page of ${shape.take(24)}") { WebSearch.extract(page) }
            }
        }
        // one long tag: the attribute reading is one pass too
        val longTag = "<meta " + "a".repeat(4000) + ">"
        quick("400 long meta tags") { WebSearch.extract("<html><head>" + longTag.repeat(100) + "</head><body>$prose</body></html>") }
        quick("attrsOf") { repeat(2000) { WebSearch.attrsOf(longTag) } }
        // the results page
        for (shape in listOf("<a class=\"result__a\" href=\"x\">", "<td class='result-snippet'>", "<a ", "<td")) {
            quick("results of ${shape.take(20)}") { WebSearch.parseDdgHtml(fill(shape)); WebSearch.parseDdgLite(fill(shape)) }
        }
    }

    @Test fun anElementNeverClosedIsKeptAndTheRestStillRead() {
        val prose = "The harbour at Gaios is on the east side of the island, sheltered by two islets, and ferries leave from the new port."
        // a <nav> that never closes: its kind is not looked for again, the page still reads
        val p = WebSearch.extract("<html><body><nav><a href=/>Home</a><p>$prose</p><script>var x = 1</script><p>$prose Twice.</p></body></html>")
        assertEquals(2, p.paragraphs.size)
        assertTrue(p.paragraphs.none { it.contains("var x") })
        // a comment that never closes takes the rest, as a browser has it
        assertEquals("<p>kept</p>", WebSearch.stripComments("<p>kept</p><!-- <p>gone</p>"))
        assertEquals("ab", WebSearch.stripComments("a<!-- x --><!----->b"))
        assertEquals("no comments", WebSearch.stripComments("no comments"))
    }

    @Test fun theCutsAreTheOnesTheExpressionsMade() {
        val drop = arrayOf("script", "nav")
        assertEquals("a b c", WebSearch.dropElements("a<script type=x>1<2</script >b<NAV><ul></ul></nav>c", drop).replace(Regex(" +"), " "))
        assertEquals("<scripted>x</scripted><navy>", WebSearch.dropElements("<scripted>x</scripted><navy>", drop)) // other names that begin the same
        assertEquals("a<script>never closed", WebSearch.dropElements("a<script>never closed", drop))
        assertEquals("inside", WebSearch.mainElement("<div><article class=x>inside</article><main>second</main></div>"))
        assertEquals("second", WebSearch.mainElement("<div><article>never closed<main>second</main></div>"))
        assertNull(WebSearch.mainElement("<div><articles>x</articles></div>"))
        assertNull(WebSearch.mainElement("<article"))
        assertEquals("a b c", WebSearch.stripTags("a<b> </b>b<br/> c"))
        assertEquals("a|b", WebSearch.stripTags("a<i>b", "|"))
        assertEquals("1 < 2 and 3 <> 4, 5 > 4", WebSearch.stripTags("1 < 2 and 3 <> 4, 5 > 4"))
        assertEquals("x", WebSearch.stripTags("<!DOCTYPE html><?xml?>x</p>"))
        assertEquals("a <b", WebSearch.stripTags("a <b"))
        assertEquals(10, WebSearch.linkChars("<p>see <a href=x>the</a> <A HREF='y'>harbour</A> <abbr>not this</abbr> <a>open"))
        assertEquals(listOf("<a href=x>" to "one", "<a>" to "two"), WebSearch.elementsNamed("<a href=x>one</a><abbr>x</abbr><a>two</a><a>open", "a", 10))
        assertEquals(1, WebSearch.elementsNamed("<a>1</a><a>2</a>", "a", 1).size)
    }

    @Test fun attributesAsABrowserReadsThem() {
        val a = WebSearch.attrsOf("<meta Property=\"og:description\" content='a \"b\" c' data-x=1 hidden content=second />")
        assertEquals("og:description", a["property"])
        assertEquals("a \"b\" c", a["content"]) // the first of a repeated name
        assertEquals("1", a["data-x"])
        assertEquals("", a["hidden"])
        assertEquals(mapOf("href" to "/a?b=c", "class" to "result__a x"), WebSearch.attrsOf("<a href=/a?b=c class = \"result__a x\">"))
        assertEquals(mapOf("datetime" to "2024-11-05"), WebSearch.attrsOf("<time datetime=\"2024-11-05\">"))
        assertEquals("unclosed value", WebSearch.attrsOf("<a title=\"unclosed value")["title"])
        assertTrue(WebSearch.attrsOf("<a>").isEmpty())
        assertTrue(WebSearch.attrsOf("").isEmpty())
        assertTrue(WebSearch.attrsOf("<a = = >").isEmpty())
        assertEquals(64, WebSearch.attrsOf("<a " + (1..200).joinToString(" ") { "k$it=v" } + ">").size)
    }

    @Test fun aNumberThatIsNoCharacterIsDropped() {
        assertEquals("ab", WebSearch.clean("a&#x7fffffff;&#1114112;&#xD800;&#0;b"))
        assertEquals("a€b", WebSearch.clean("a&#x20AC;b"))
        assertEquals("😀", WebSearch.clean("&#128512;"))
    }
}
