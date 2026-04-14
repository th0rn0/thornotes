package com.thornotes.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.assertFalse
import org.junit.Test

class UrlValidatorTest {

    // --- validate() ---

    @Test
    fun `empty string returns error`() {
        assertEquals("Please enter a server URL.", UrlValidator.validate(""))
    }

    @Test
    fun `blank string returns error`() {
        assertEquals("Please enter a server URL.", UrlValidator.validate("   "))
    }

    @Test
    fun `plain hostname without scheme returns scheme error`() {
        assertEquals(
            "URL must start with http:// or https://",
            UrlValidator.validate("example.com"),
        )
    }

    @Test
    fun `ftp scheme returns scheme error`() {
        assertEquals(
            "URL must start with http:// or https://",
            UrlValidator.validate("ftp://example.com"),
        )
    }

    @Test
    fun `http url returns null (valid)`() {
        assertNull(UrlValidator.validate("http://192.168.1.100:8080"))
    }

    @Test
    fun `https url returns null (valid)`() {
        assertNull(UrlValidator.validate("https://notes.example.com"))
    }

    @Test
    fun `http url with trailing slash returns null (valid)`() {
        assertNull(UrlValidator.validate("http://localhost:8080/"))
    }

    @Test
    fun `https url with path returns null (valid)`() {
        assertNull(UrlValidator.validate("https://notes.example.com/thornotes"))
    }

    // --- isValid() ---

    @Test
    fun `isValid returns false for empty string`() {
        assertFalse(UrlValidator.isValid(""))
    }

    @Test
    fun `isValid returns false for missing scheme`() {
        assertFalse(UrlValidator.isValid("example.com"))
    }

    @Test
    fun `isValid returns true for http url`() {
        assertTrue(UrlValidator.isValid("http://localhost:8080"))
    }

    @Test
    fun `isValid returns true for https url`() {
        assertTrue(UrlValidator.isValid("https://notes.example.com"))
    }
}
