package com.thornotes.app

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import com.thornotes.app.databinding.ActivitySetupBinding

class SetupActivity : AppCompatActivity() {

    companion object {
        const val EXTRA_ERROR = "error"
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val binding = ActivitySetupBinding.inflate(layoutInflater)
        setContentView(binding.root)

        // Pre-fill any saved URL.
        val prefs = getSharedPreferences("thornotes", Context.MODE_PRIVATE)
        val saved = prefs.getString("serverUrl", "")
        if (!saved.isNullOrBlank()) binding.urlInput.setText(saved)

        // Show error passed from MainActivity on connection failure.
        intent.getStringExtra(EXTRA_ERROR)?.let { err ->
            binding.errorText.text = err
            binding.errorText.visibility = View.VISIBLE
        }

        binding.connectBtn.setOnClickListener {
            binding.errorText.visibility = View.GONE
            val url = binding.urlInput.text.toString().trim()

            val error = when {
                url.isBlank() -> "Please enter a server URL."
                !url.startsWith("http://") && !url.startsWith("https://") ->
                    "URL must start with http:// or https://"
                else -> null
            }
            if (error != null) {
                binding.errorText.text = error
                binding.errorText.visibility = View.VISIBLE
                return@setOnClickListener
            }

            prefs.edit().putString("serverUrl", url).apply()
            startActivity(Intent(this, MainActivity::class.java))
            finish()
        }

        // Allow Enter key to connect.
        binding.urlInput.setOnEditorActionListener { _, _, _ ->
            binding.connectBtn.performClick()
            true
        }
    }
}
