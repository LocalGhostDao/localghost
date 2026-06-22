package com.localghost.app.security

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.spec.ECGenParameterSpec

/**
 * The phone's device identity keypair — EC P-256 in the AndroidKeyStore, StrongBox-backed
 * when the hardware allows. This is the key that will sign the CSR ghost.secd enrolls, and
 * that authenticates the mTLS channel. Private key never leaves secure hardware.
 */
object DeviceIdentity {
    private const val ALIAS = "localghost.device"

    fun ensureKey() {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (ks.containsAlias(ALIAS)) return

        val kpg = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore")
        try {
            kpg.initialize(spec(strongBox = true)); kpg.generateKeyPair()
        } catch (e: Exception) {
            // StrongBox unavailable on this device — fall back to TEE-backed.
            kpg.initialize(spec(strongBox = false)); kpg.generateKeyPair()
        }
    }

    private fun spec(strongBox: Boolean): KeyGenParameterSpec =
        KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY)
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256)
            .apply { if (strongBox) setIsStrongBoxBacked(true) }
            .build()
}
