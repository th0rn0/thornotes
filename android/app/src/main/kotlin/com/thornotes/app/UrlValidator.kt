package com.thornotes.app

object UrlValidator {
    fun validate(url: String): String? = when {
        url.isBlank() -> "Please enter a server URL."
        !url.startsWith("http://") && !url.startsWith("https://") ->
            "URL must start with http:// or https://"
        else -> null
    }

    fun isValid(url: String): Boolean = validate(url) == null
}
