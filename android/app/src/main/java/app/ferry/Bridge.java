package app.ferry;

import android.app.DownloadManager;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.PowerManager;
import android.provider.Settings;
import android.util.Base64;
import android.webkit.JavascriptInterface;
import android.widget.Toast;

import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/** Exposed to the web UI as window.FerryAndroid. */
final class Bridge {
    private final MainActivity act;
    private final Map<String, Saver> saves = new HashMap<>();
    private int nextId = 1;

    Bridge(MainActivity act) {
        this.act = act;
    }

    // ---- Saving files the web UI unlocked (encrypted shares) ----

    @JavascriptInterface
    public synchronized String beginSave(String name, String mime) {
        try {
            String id = String.valueOf(nextId++);
            saves.put(id, Saver.open(act, name, mime));
            return id;
        } catch (Exception e) {
            toast("Couldn't save: " + e.getMessage());
            return "";
        }
    }

    @JavascriptInterface
    public synchronized void appendChunk(String id, String base64) {
        Saver s = saves.get(id);
        if (s == null) return;
        try {
            s.out.write(Base64.decode(base64, Base64.DEFAULT));
        } catch (Exception e) {
            s.abort();
            saves.remove(id);
            toast("Couldn't save: " + e.getMessage());
        }
    }

    @JavascriptInterface
    public synchronized void finishSave(String id) {
        Saver s = saves.remove(id);
        if (s == null) return;
        try {
            s.finish();
            toast("Saved " + s.name + " to Downloads/Ferry");
        } catch (Exception e) {
            s.abort();
            toast("Couldn't save: " + e.getMessage());
        }
    }

    // ---- Files shared to Ferry from other apps ----

    @JavascriptInterface
    public String sharedItems() {
        return act.inbox.describe();
    }

    @JavascriptInterface
    public String readShared(int index, double offset, int max) {
        try {
            return act.inbox.read(index, (long) offset, max);
        } catch (Exception e) {
            return "";
        }
    }

    @JavascriptInterface
    public void clearShared() {
        act.inbox.clear();
    }

    // ---- Clipboard, Downloads, hotspot ----

    @JavascriptInterface
    public String getClipboard() {
        final String[] out = {""};
        final CountDownLatch done = new CountDownLatch(1);
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                try {
                    ClipboardManager cm = (ClipboardManager) act.getSystemService(Context.CLIPBOARD_SERVICE);
                    ClipData clip = cm.getPrimaryClip();
                    if (clip != null && clip.getItemCount() > 0) {
                        CharSequence t = clip.getItemAt(0).coerceToText(act);
                        if (t != null) out[0] = t.toString();
                    }
                } catch (Exception ignored) {
                }
                done.countDown();
            }
        });
        try {
            done.await(2, TimeUnit.SECONDS);
        } catch (InterruptedException ignored) {
        }
        return out[0];
    }

    @JavascriptInterface
    public void setClipboard(final String text) {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                ClipboardManager cm = (ClipboardManager) act.getSystemService(Context.CLIPBOARD_SERVICE);
                cm.setPrimaryClip(ClipData.newPlainText("Ferry", text));
            }
        });
    }

    @JavascriptInterface
    public void openDownloads() {
        try {
            act.startActivity(new Intent(DownloadManager.ACTION_VIEW_DOWNLOADS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
        } catch (Exception e) {
            toast("Open the Files app and look in Downloads/Ferry");
        }
    }

    @JavascriptInterface
    public boolean hotspotSupported() {
        return Hotspot.supported();
    }

    @JavascriptInterface
    public String hotspotState() {
        return act.hotspot.state();
    }

    @JavascriptInterface
    public void startHotspot() {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                act.startHotspotWithPermission();
            }
        });
    }

    @JavascriptInterface
    public void stopHotspot() {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                act.hotspot.stop();
            }
        });
    }

    // ---- Hotspot join ----

    @JavascriptInterface
    public void joinHotspot(final String ssid, final String pass, final String url) {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                act.joinHotspot(ssid, pass, url);
            }
        });
    }

    @JavascriptInterface
    public void leaveHotspot() {
        act.leaveHotspot(true);
    }

    @JavascriptInterface
    public boolean joinedHotspot() {
        return act.joined;
    }

    // ---- Appearance ----

    @JavascriptInterface
    public void setDarkMode(final boolean dark) {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                act.applyTheme(dark);
            }
        });
    }

    // ---- Background running ----

    @JavascriptInterface
    public boolean batteryRestricted() {
        if (Build.VERSION.SDK_INT < 23) return false;
        PowerManager pm = (PowerManager) act.getSystemService(Context.POWER_SERVICE);
        return !pm.isIgnoringBatteryOptimizations(act.getPackageName());
    }

    @JavascriptInterface
    public void requestBatteryExemption() {
        if (Build.VERSION.SDK_INT < 23) return;
        try {
            act.startActivity(new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                    Uri.parse("package:" + act.getPackageName())));
        } catch (Exception e) {
            try {
                act.startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
            } catch (Exception ignored) {
                toast("Open Settings → Apps → Ferry → Battery and choose Unrestricted");
            }
        }
    }

    @JavascriptInterface
    public boolean startOnBoot() {
        return act.getSharedPreferences(BootReceiver.PREFS, Context.MODE_PRIVATE).getBoolean(BootReceiver.KEY, true);
    }

    @JavascriptInterface
    public void setStartOnBoot(boolean on) {
        act.getSharedPreferences(BootReceiver.PREFS, Context.MODE_PRIVATE).edit().putBoolean(BootReceiver.KEY, on).apply();
    }

    private void toast(final String msg) {
        act.runOnUiThread(new Runnable() {
            @Override
            public void run() {
                Toast.makeText(act, msg, Toast.LENGTH_LONG).show();
            }
        });
    }
}
