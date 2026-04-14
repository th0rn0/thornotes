package com.thornotes.app

object UrlValidator {
    private val HTTP = "http://"
    private val HTTPS = "https://"

    fun validate(url: String): String? {
        val lower = url.trim().lowercase()
        return when {
            url.isBlank() -> "Please enter a server URL."
            !lower.startsWith(HTTP) && !lower.startsWith(HTTPS) ->
                "URL must start with http:// or https://"
            lower == HTTP || lower == HTTPS ->
                "Please enter a complete server URL."
            else -> null
        }
    }

    fun isValid(url: String): Boolean = validate(url) == null
}
