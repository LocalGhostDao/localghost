package com.localghost.app.ui

sealed interface Loadable<out T> {
    data object Loading : Loadable<Nothing>
    data class Loaded<T>(val value: T) : Loadable<T>
    data class Failed(val reason: String) : Loadable<Nothing>
}
