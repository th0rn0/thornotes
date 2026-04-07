package com.thornotes.app

import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.Menu
import android.view.MenuItem
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity
import com.thornotes.app.databinding.ActivityMainBinding

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val serverUrl = savedServerUrl()
        if (serverUrl == null) {
            openSetup()
            return
        }

        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        setSupportActionBar(binding.toolbar)

        configureWebView(binding.webview)
        binding.webview.loadUrl(serverUrl)

        // Handle back navigation inside the WebView.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (binding.webview.canGoBack()) {
                    binding.webview.goBack()
                } else {
                    isEnabled = false
                    onBackPressedDispatcher.onBackPressed()
                }
            }
        })
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun configureWebView(wv: WebView) {
        wv.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            databaseEnabled = true
            allowFileAccess = false
            cacheMode = WebSettings.LOAD_DEFAULT
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }

        wv.webViewClient = object : WebViewClient() {
            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: WebResourceError,
            ) {
                if (request.isForMainFrame) {
                    openSetup("Could not reach ${request.url}\n${error.description}")
                }
            }
        }

        // Persist cookies across sessions (needed for login sessions).
        android.webkit.CookieManager.getInstance().setAcceptCookie(true)
        android.webkit.CookieManager.getInstance().setAcceptThirdPartyCookies(wv, false)
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.main_menu, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        return when (item.itemId) {
            R.id.action_reload -> {
                binding.webview.reload()
                true
            }
            R.id.action_change_server -> {
                openSetup()
                true
            }
            else -> super.onOptionsItemSelected(item)
        }
    }

    private fun savedServerUrl(): String? {
        val url = getSharedPreferences("thornotes", Context.MODE_PRIVATE)
            .getString("serverUrl", null)
        return if (url.isNullOrBlank()) null else url
    }

    private fun openSetup(error: String? = null) {
        val intent = Intent(this, SetupActivity::class.java)
        if (error != null) intent.putExtra(SetupActivity.EXTRA_ERROR, error)
        startActivity(intent)
        finish()
    }
}
