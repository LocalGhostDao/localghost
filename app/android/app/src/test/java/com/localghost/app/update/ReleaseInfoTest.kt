package com.localghost.app.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ReleaseInfoTest {
    private val manifest = """# LocalGhost Mirror Manifest
# Build: 20260930T120000Z
# Signed: 2026-09-30T12:00:00Z

fa8a3dbf0e91040949414b86cf5a67b9971c6ee79c18c95e55410d21b2d888ff  /20260930T120000Z/server/NOTICE.txt
8f8a92015825604a3f73c0d2dafd4139c2e115706476cbc64a3ef467916071b1  /20260930T120000Z/server/RELEASE.txt
a23072b4dd44e7c82758696f242d74d8c51bac6895bd4907fa05ef72ed8c8ccd  /20260930T120000Z/server/localghost-server-0.9.3-linux-amd64.tar.gz
b23072b4dd44e7c82758696f242d74d8c51bac6895bd4907fa05ef72ed8c8ccd  /20260930T120000Z/geo/allCountries.zip
c23072b4dd44e7c82758696f242d74d8c51bac6895bd4907fa05ef72ed8c8ccd  /20260901T000000Z/server/old.tar.gz
d23072b4dd44e7c82758696f242d74d8c51bac6895bd4907fa05ef72ed8c8ccd  /20260930T120000Z/server/../../etc/passwd
"""

    @Test fun readsTheServerSetOfTheCurrentBuild() {
        val m = ReleaseInfo.manifest(manifest)!!
        assertEquals("20260930T120000Z", m.build)
        assertEquals(listOf("NOTICE.txt", "RELEASE.txt", "localghost-server-0.9.3-linux-amd64.tar.gz"), m.server.map { it.name })
        assertEquals("/20260930T120000Z/server/RELEASE.txt", m.server[1].path)
        assertNull(ReleaseInfo.manifest("not a manifest"))
    }

    @Test fun readsTheReleaseNotes() {
        val r = ReleaseInfo.release("version=0.9.3\ncommit=e830701\ndate=2026-09-30T11:17:32Z\nbundle=b.tar.gz\nsince=v0.9.2\nchanges:\n  e830701 the vault rings\n  a1b2c3d trail questions\n")!!
        assertEquals("0.9.3", r.version)
        assertEquals(listOf("e830701 the vault rings", "a1b2c3d trail questions"), r.changes)
        assertNull(ReleaseInfo.release("commit=x"))
        val w = ReleaseInfo.release("version=0.0.1\nname=wisp\ncommit=abc\nchanges:\n  abc the first release\n")!!
        assertEquals("wisp", w.name)
        assertEquals("wisp 0.0.1", w.label)
        assertEquals("0.9.3", ReleaseInfo.release("version=0.9.3\n")!!.label)
    }

    @Test fun keepsOnlyTheHeaderAndTheServerSetWhileReading() {
        // the manifest lists every set; a phone reading it line by line keeps these and drops the rest
        val kept = manifest.lines().filter(ReleaseInfo::keep)
        assertTrue(kept.contains("# Build: 20260930T120000Z"))
        assertTrue(kept.contains(""))
        assertTrue(kept.any { it.endsWith("/server/RELEASE.txt") })
        assertFalse(kept.any { it.contains("/geo/") })
        assertFalse(ReleaseInfo.keep("e23072b4dd44e7c82758696f242d74d8c51bac6895bd4907fa05ef72ed8c8ccd  /20260930T120000Z/elevation/Copernicus_DSM_COG_10_N51_00_W001_00_DEM.tif"))
        // what is kept still reads as the manifest
        val m = ReleaseInfo.manifest(kept.joinToString("\n"))!!
        assertEquals(3, m.server.size)
        assertEquals("0.9.3", ReleaseInfo.release("version=0.9.3\ngo=1.27.1\n")!!.version)
        assertEquals("1.27.1", ReleaseInfo.release("version=0.9.3\ngo=1.27.1\n")!!.go)
    }

    @Test fun describesABuild() {
        assertEquals("4 Oct 2026, 17:12 UTC", ReleaseInfo.at("2026-10-04T17:12:00Z"))
        assertEquals("4 Oct 2026, 17:12 +01:00", ReleaseInfo.at("2026-10-04T17:12:00+01:00"))
        assertEquals("30 Sep 2026", ReleaseInfo.at("2026-09-30"))
        assertEquals("yesterday", ReleaseInfo.at("yesterday"))
        assertEquals("1062b7f", ReleaseInfo.short("1062b7f9a1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6"))
        assertEquals("wisp 0.0.4 (1062b7f), built 4 Oct 2026, 17:12 UTC, Go 1.27.1",
            ReleaseInfo.describe("wisp 0.0.4", "1062b7f9a1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6", "2026-10-04T17:12:00Z", "go1.27.1"))
        assertEquals("v0.0.3-12-gfb28812-dirty, built 4 Oct 2026, 18:02 UTC, Go 1.27.1",
            ReleaseInfo.describe("v0.0.3-12-gfb28812-dirty", "", "2026-10-04T18:02:11Z", "go1.27.1"))
        assertEquals("0.0.3", ReleaseInfo.describe("0.0.3", "", "", "")) // an older box says only its version
    }

    @Test fun comparesVersions() {
        assertTrue(ReleaseInfo.newer("0.9.3", "0.9.2"))
        assertTrue(ReleaseInfo.newer("0.10.0", "0.9.9"))
        assertFalse(ReleaseInfo.newer("0.9.3", "0.9.3"))
        assertFalse(ReleaseInfo.newer("0.9.2", "0.9.3"))
        // a build from source: its tag and the commits on top
        assertFalse(ReleaseInfo.newer("0.9.2", "v0.9.2-5-gabc1234-dirty"))
        assertTrue(ReleaseInfo.newer("0.9.3", "v0.9.2-5-gabc1234"))
        assertTrue(ReleaseInfo.newer("0.9.3", "dev"))
        assertTrue(ReleaseInfo.newer("0.9.3", "abc1234"))   // git describe --always, no tag yet
        assertTrue(ReleaseInfo.newer("0.9.3", "1234567"))   // a hash that happens to be all digits
        assertFalse(ReleaseInfo.newer("nonsense", "0.9.2"))
    }
}
