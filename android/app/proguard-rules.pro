# Keep WebView JavaScript interface if ever added
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}
