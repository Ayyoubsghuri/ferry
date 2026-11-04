package app.ferry;

import android.Manifest;
import android.app.Activity;
import android.content.ClipData;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.content.res.Configuration;
import android.graphics.Insets;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.net.Uri;
import android.net.wifi.WifiConfiguration;
import android.net.wifi.WifiManager;
import android.net.wifi.WifiNetworkSpecifier;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.webkit.ConsoleMessage;
import android.webkit.DownloadListener;
import android.webkit.PermissionRequest;
import android.webkit.SslErrorHandler;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.net.http.SslError;
import android.widget.FrameLayout;

import java.net.HttpURLConnection;
import java.net.URL;
import java.util.ArrayList;
import java.util.List;

/** Hosts the Ferry web UI, served by the bundled server running on this phone. */
public class MainActivity extends Activity {
    static final int PORT = 7070;
    static final String HOME = "http://127.0.0.1:" + PORT + "/";
    private static final int REQ_FILES = 1;

    private WebView web;
    private ValueCallback<Uri[]> fileCallback;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private boolean loaded;
    final SharedInbox inbox = new SharedInbox(this);
    Hotspot hotspot;
    private View rootView;
    static volatile boolean visible;
    private PermissionRequest pendingCamera;
    private static final int REQ_CAMERA = 11;
    private static final int REQ_HOTSPOT = 12;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        FrameLayout root = new FrameLayout(this);
        rootView = root;
        web = new WebView(this);
        root.addView(web, new FrameLayout.LayoutParams(-1, -1));
        setContentView(root);
        fitSystemBars(root);

        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        s.setSupportZoom(false);
        s.setSupportMultipleWindows(false);
        s.setMixedContentMode(WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE);
        s.setUserAgentString(s.getUserAgentString() + " FerryApp/2");

        web.addJavascriptInterface(new Bridge(this), "FerryAndroid");
        web.setWebViewClient(new Client());
        web.setWebChromeClient(new Chrome());
        web.setDownloadListener(new DownloadListener() {
            @Override
            public void onDownloadStart(String url, String userAgent, String contentDisposition,
                                        String mimetype, long contentLength) {
                Downloader.start(MainActivity.this, url, contentDisposition, mimetype);
            }
        });

        hotspot = new Hotspot(this, new Hotspot.Listener() {
            @Override
            public void onState(final String json) {
                ServerService.refreshAddresses(MainActivity.this);
                ui.post(new Runnable() {
                    @Override
                    public void run() {
                        web.evaluateJavascript("window.dispatchEvent(new CustomEvent('ferry-hotspot',{detail:"
                                + json + "}))", null);
                    }
                });
            }
        });
        applyTheme(darkMode());
        askPermissions();
        startServer();
        web.loadDataWithBaseURL(null, splash("Starting Ferry…"), "text/html", "utf-8", null);
        waitForServerThenLoad();
        inbox.take(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        if (inbox.take(intent) && loaded) {
            web.evaluateJavascript("window.dispatchEvent(new Event('ferry-shared'))", null);
        }
    }

    @Override
    protected void onPause() {
        super.onPause();
        visible = false;
    }

    @Override
    protected void onResume() {
        super.onResume();
        visible = true;
        startServer(); // in case it was stopped from the notification
    }

    void startServer() {
        Intent i = new Intent(this, ServerService.class);
        if (Build.VERSION.SDK_INT >= 26) startForegroundService(i);
        else startService(i);
    }

    private void waitForServerThenLoad() {
        new Thread(new Runnable() {
            @Override
            public void run() {
                for (int i = 0; i < 100; i++) {
                    if (ping()) {
                        ui.post(new Runnable() {
                            @Override
                            public void run() {
                                loaded = true;
                                web.loadUrl(HOME);
                            }
                        });
                        return;
                    }
                    try {
                        Thread.sleep(150);
                    } catch (InterruptedException e) {
                        return;
                    }
                }
                ui.post(new Runnable() {
                    @Override
                    public void run() {
                        web.loadDataWithBaseURL(null,
                                splash("Ferry couldn't start its server. Close the app and open it again."),
                                "text/html", "utf-8", null);
                    }
                });
            }
        }).start();
    }

    static boolean ping() {
        try {
            HttpURLConnection c = (HttpURLConnection) new URL(HOME + "api/info").openConnection();
            c.setConnectTimeout(500);
            c.setReadTimeout(800);
            int code = c.getResponseCode();
            c.disconnect();
            return code == 200;
        } catch (Exception e) {
            return false;
        }
    }

    boolean darkMode() {
        return getSharedPreferences(BootReceiver.PREFS, MODE_PRIVATE).getBoolean("dark", false);
    }

    /** Matches the status and navigation bars to Ferry's light or dark look. */
    void applyTheme(boolean dark) {
        getSharedPreferences(BootReceiver.PREFS, MODE_PRIVATE).edit().putBoolean("dark", dark).apply();
        int bg = dark ? 0xFF111815 : 0xFFE8ECE5;
        getWindow().setStatusBarColor(bg);
        getWindow().setNavigationBarColor(bg);
        getWindow().getDecorView().setBackgroundColor(bg);
        if (rootView != null) rootView.setBackgroundColor(bg);
        if (Build.VERSION.SDK_INT >= 30) {
            WindowInsetsController c = getWindow().getInsetsController();
            if (c != null) {
                int light = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
                        | WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS;
                c.setSystemBarsAppearance(dark ? 0 : light, light);
            }
        } else if (Build.VERSION.SDK_INT >= 23) {
            View d = getWindow().getDecorView();
            int f = d.getSystemUiVisibility();
            f = dark ? (f & ~View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR) : (f | View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR);
            d.setSystemUiVisibility(f);
        }
    }

    private String splash(String msg) {
        boolean dark = darkMode();
        String bg = dark ? "#111815" : "#E8ECE5";
        String fg = dark ? "#E7EDE8" : "#17211E";
        return "<html><body style='margin:0;height:100vh;display:flex;align-items:center;justify-content:center;"
                + "background:" + bg + ";color:" + fg + ";font-family:sans-serif;text-align:center;padding:24px'>"
                + "<p>" + msg + "</p></body></html>";
    }

    private void fitSystemBars(View root) {
        if (Build.VERSION.SDK_INT >= 30) {
            root.setOnApplyWindowInsetsListener(new View.OnApplyWindowInsetsListener() {
                @Override
                public WindowInsets onApplyWindowInsets(View v, WindowInsets insets) {
                    Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
                    v.setPadding(bars.left, bars.top, bars.right, bars.bottom);
                    return WindowInsets.CONSUMED;
                }
            });

        }
    }

    private void askPermissions() {
        List<String> need = new ArrayList<>();
        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            need.add(Manifest.permission.POST_NOTIFICATIONS);
        }
        if (Build.VERSION.SDK_INT >= 23 && Build.VERSION.SDK_INT < 29
                && checkSelfPermission(Manifest.permission.WRITE_EXTERNAL_STORAGE) != PackageManager.PERMISSION_GRANTED) {
            need.add(Manifest.permission.WRITE_EXTERNAL_STORAGE);
        }
        if (!need.isEmpty() && Build.VERSION.SDK_INT >= 23) {
            requestPermissions(need.toArray(new String[0]), 7);
        }
    }

    void startHotspotWithPermission() {
        if (Hotspot.hasPermission(this)) {
            hotspot.start();
        } else if (Build.VERSION.SDK_INT >= 23) {
            requestPermissions(Hotspot.permissions(), REQ_HOTSPOT);
        }
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions, int[] grantResults) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults);
        boolean granted = grantResults.length > 0;
        for (int g : grantResults) granted &= g == PackageManager.PERMISSION_GRANTED;
        if (requestCode == REQ_CAMERA && pendingCamera != null) {
            if (granted) pendingCamera.grant(new String[]{PermissionRequest.RESOURCE_VIDEO_CAPTURE});
            else pendingCamera.deny();
            pendingCamera = null;
        } else if (requestCode == REQ_HOTSPOT) {
            if (granted) hotspot.start();
            else hotspot.stop();
        }
    }

    static boolean isPrivateHost(String host) {
        if (host == null) return false;
        if (host.equals("localhost") || host.startsWith("127.") || host.startsWith("10.")
                || host.startsWith("192.168.") || host.endsWith(".local")) return true;
        if (host.startsWith("172.")) {
            try {
                int second = Integer.parseInt(host.split("\\.")[1]);
                return second >= 16 && second <= 31;
            } catch (Exception e) {
                return false;
            }
        }
        return false;
    }

    private class Client extends WebViewClient {
        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
            Uri u = request.getUrl();
            String scheme = u.getScheme();
            if (("http".equals(scheme) || "https".equals(scheme)) && isPrivateHost(u.getHost())) {
                return false; // Ferry pages, here or on another device nearby
            }
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, u));
            } catch (Exception ignored) {
            }
            return true;
        }

        @Override
        public void onReceivedSslError(WebView view, SslErrorHandler handler, SslError error) {
            // Other Ferry devices use self-signed certificates on the local network.
            Uri u = Uri.parse(error.getUrl());
            if (isPrivateHost(u.getHost())) handler.proceed();
            else handler.cancel();
        }

        @Override
        public void onReceivedError(final WebView view, WebResourceRequest request, WebResourceError error) {
            if (request.isForMainFrame() && request.getUrl().toString().startsWith(HOME)) {
                ui.postDelayed(new Runnable() {
                    @Override
                    public void run() {
                        view.loadUrl(HOME);
                    }
                }, 1000);
            }
        }

        @Override
        public void onPageFinished(WebView view, String url) {
            if (url.startsWith(HOME) && inbox.hasPending()) {
                view.evaluateJavascript("window.dispatchEvent(new Event('ferry-shared'))", null);
            }
        }
    }

    private class Chrome extends WebChromeClient {
        @Override
        public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback, FileChooserParams params) {
            if (fileCallback != null) fileCallback.onReceiveValue(null);
            fileCallback = callback;
            Intent i = new Intent(Intent.ACTION_GET_CONTENT);
            i.addCategory(Intent.CATEGORY_OPENABLE);
            i.setType("*/*");
            i.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, params.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE);
            try {
                startActivityForResult(Intent.createChooser(i, "Choose files to send"), REQ_FILES);
            } catch (Exception e) {
                fileCallback = null;
                return false;
            }
            return true;
        }

        @Override
        public void onPermissionRequest(final PermissionRequest request) {
            // The QR scanner in the web UI asks for the camera.
            boolean wantsCamera = false;
            for (String r : request.getResources()) {
                if (PermissionRequest.RESOURCE_VIDEO_CAPTURE.equals(r)) wantsCamera = true;
            }
            if (!wantsCamera || !"127.0.0.1".equals(request.getOrigin().getHost())) {
                request.deny();
                return;
            }
            if (Build.VERSION.SDK_INT < 23
                    || checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                request.grant(new String[]{PermissionRequest.RESOURCE_VIDEO_CAPTURE});
            } else {
                pendingCamera = request;
                requestPermissions(new String[]{Manifest.permission.CAMERA}, REQ_CAMERA);
            }
        }

        @Override
        public boolean onConsoleMessage(ConsoleMessage m) {
            return true;
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != REQ_FILES || fileCallback == null) return;
        Uri[] result = null;
        if (resultCode == RESULT_OK && data != null) {
            ClipData clip = data.getClipData();
            if (clip != null) {
                result = new Uri[clip.getItemCount()];
                for (int i = 0; i < clip.getItemCount(); i++) result[i] = clip.getItemAt(i).getUri();
            } else if (data.getData() != null) {
                result = new Uri[]{data.getData()};
            }
        }
        fileCallback.onReceiveValue(result);
        fileCallback = null;
    }

    // ---- Joining another phone's Ferry hotspot (scan its QR) ----

    private ConnectivityManager.NetworkCallback joinCallback;
    volatile boolean joined;

    void emitJoin(final String state, final String message) {
        ui.post(new Runnable() {
            @Override
            public void run() {
                String js = "window.dispatchEvent(new CustomEvent('ferry-join',{detail:{state:" + jsq(state)
                        + ",message:" + jsq(message == null ? "" : message) + "}}))";
                web.evaluateJavascript(js, null);
            }
        });
    }

    static String jsq(String s) {
        return "'" + s.replace("\\", "\\\\").replace("'", "\\'").replace("\n", " ") + "'";
    }

    @SuppressWarnings("deprecation")
    void joinHotspot(final String ssid, final String pass, final String url) {
        leaveHotspot(false);
        final ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
        emitJoin("connecting", null);
        if (Build.VERSION.SDK_INT >= 29) {
            WifiNetworkSpecifier spec = new WifiNetworkSpecifier.Builder()
                    .setSsid(ssid).setWpa2Passphrase(pass).build();
            NetworkRequest req = new NetworkRequest.Builder()
                    .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                    .removeCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    .setNetworkSpecifier(spec).build();
            joinCallback = new ConnectivityManager.NetworkCallback() {
                @Override
                public void onAvailable(Network network) {
                    // Route this app's traffic (the page and downloads) over the hotspot.
                    cm.bindProcessToNetwork(network);
                    joined = true;
                    ui.post(new Runnable() {
                        @Override
                        public void run() {
                            web.loadUrl(url);
                        }
                    });
                }

                @Override
                public void onUnavailable() {
                    emitJoin("error", "Couldn't connect to " + ssid + ". Make sure the other phone is still showing its code.");
                }

                @Override
                public void onLost(Network network) {
                    if (joined) leaveHotspot(true);
                }
            };
            try {
                cm.requestNetwork(req, joinCallback, 60000);
            } catch (Exception e) {
                emitJoin("error", "This phone couldn't join the hotspot: " + e.getMessage());
            }
        } else {
            // Android 9 and older: connect the usual way, then open the page once connected.
            try {
                WifiManager wm = (WifiManager) getApplicationContext().getSystemService(WIFI_SERVICE);
                WifiConfiguration c = new WifiConfiguration();
                c.SSID = "\"" + ssid + "\"";
                c.preSharedKey = "\"" + pass + "\"";
                int id = wm.addNetwork(c);
                wm.disconnect();
                wm.enableNetwork(id, true);
                wm.reconnect();
                joined = true;
                new Thread(new Runnable() {
                    @Override
                    public void run() {
                        for (int i = 0; i < 40; i++) {
                            try {
                                Thread.sleep(500);
                                java.net.HttpURLConnection h = (java.net.HttpURLConnection) new java.net.URL(url + "api/info").openConnection();
                                h.setConnectTimeout(700);
                                if (h.getResponseCode() == 200) {
                                    ui.post(new Runnable() {
                                        @Override
                                        public void run() {
                                            web.loadUrl(url);
                                        }
                                    });
                                    return;
                                }
                            } catch (Exception ignored) {
                            }
                        }
                        emitJoin("error", "Couldn't reach the other phone. Connect to " + ssid + " in Wi-Fi settings and try again.");
                    }
                }).start();
            } catch (Exception e) {
                emitJoin("error", "Connect to " + ssid + " in Wi-Fi settings (password " + pass + ").");
            }
        }
    }

    void leaveHotspot(boolean reload) {
        ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
        if (Build.VERSION.SDK_INT >= 23) cm.bindProcessToNetwork(null);
        if (joinCallback != null) {
            try {
                cm.unregisterNetworkCallback(joinCallback);
            } catch (Exception ignored) {
            }
            joinCallback = null;
        }
        boolean was = joined;
        joined = false;
        if (reload && was) {
            ui.post(new Runnable() {
                @Override
                public void run() {
                    web.loadUrl(HOME);
                }
            });
        }
    }

    @Override
    public void onBackPressed() {
        if (joined) {
            leaveHotspot(true);
            return;
        }
        if (web.canGoBack()) web.goBack();
        else moveTaskToBack(true); // keep sharing in the background
    }
}
