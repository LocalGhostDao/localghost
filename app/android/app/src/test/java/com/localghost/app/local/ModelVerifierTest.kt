package com.localghost.app.local

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Pins the model integrity check. A downloaded model is accepted only if its SHA-256 matches the
 * hash the box published. These use known NIST/standard vectors so a regression in the hex
 * formatting, byte order, or comparison is caught.
 */
class ModelVerifierTest {

    private fun stream(s: String) = s.toByteArray(Charsets.UTF_8).inputStream()

    // Known SHA-256 vectors.
    private val emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    private val abcHash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

    @Test fun sha256_of_empty_input() {
        assertEquals(emptyHash, ModelVerifier.sha256Hex(stream("")))
    }

    @Test fun sha256_of_abc() {
        assertEquals(abcHash, ModelVerifier.sha256Hex(stream("abc")))
    }

    @Test fun matches_accepts_correct_hash() {
        assertTrue(ModelVerifier.matches(stream("abc"), abcHash))
    }

    @Test fun matches_is_case_insensitive() {
        assertTrue(ModelVerifier.matches(stream("abc"), abcHash.uppercase()))
    }

    @Test fun matches_rejects_wrong_hash() {
        assertFalse(ModelVerifier.matches(stream("abc"), emptyHash))
    }

    @Test fun matches_rejects_tampered_content() {
        // Same expected hash, but one byte of content changed -> must reject.
        assertFalse(ModelVerifier.matches(stream("abd"), abcHash))
    }

    @Test fun matches_trims_surrounding_whitespace_in_expected() {
        assertTrue(ModelVerifier.matches(stream("abc"), "  $abcHash\n"))
    }

    @Test fun matches_treats_null_or_blank_expected_as_not_verified() {
        // No published hash -> matches() returns false (the Worker handles the "no hash" case with
        // its own sha != null guard; matches() never claims an unverified file is verified).
        assertFalse(ModelVerifier.matches(stream("abc"), null))
        assertFalse(ModelVerifier.matches(stream("abc"), ""))
        assertFalse(ModelVerifier.matches(stream("abc"), "   "))
    }
}
