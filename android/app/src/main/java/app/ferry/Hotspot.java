package app.ferry;

import android.Manifest;
import android.content.Context;
import android.content.pm.PackageManager;
import android.net.wifi.SoftApConfiguration;
import android.net.wifi.WifiConfiguration;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;

import org.json.JSONObject;

import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.NetworkInterface;
import java.util.Collections;
import java.util.HashSet;
import java.util.Set;

/**
 * Starts Android's "local-only hotspot": a Wi-Fi network created by this phone with no
 * internet access, so devices can share without a router.
 */
final class Hotspot {
    interface Listener {
        void onState(String json);
    }

    private final Context ctx;
    private final Listener listener;
    private WifiManager.LocalOnlyHotspotReservation reservation;
    private String state = "{\"state\":\"off\"}";

    Hotspot(Context ctx, Listener listener) {
        this.ctx = ctx.getApplicationContext();
        this.listener = listener;
    }

    static boolean supported() {
        return Build.VERSION.SDK_INT >= 26;
    }

    static String[] permissions() {
        if (Build.VERSION.SDK_INT >= 33) return new String[]{Manifest.permission.NEARBY_WIFI_DEVICES};
        return new String[]{Manifest.permission.ACCESS_FINE_LOCATION};
    }

    static boolean hasPermission(Context c) {
        if (Build.VERSION.SDK_INT < 23) return true;
        for (String p : permissions()) {
            if (c.checkSelfPermission(p) != PackageManager.PERMISSION_GRANTED) return false;
        }
        return true;
    }

    String state() {
        return state;
    }

    private Set<String> before = new HashSet<>();

    static Set<String> ipv4s() {
        Set<String> out = new HashSet<>();
        try {
            for (NetworkInterface ni : Collections.list(NetworkInterface.getNetworkInterfaces())) {
                if (!ni.isUp() || ni.isLoopback()) continue;
                for (InetAddress a : Collections.list(ni.getInetAddresses())) {
                    if (a instanceof Inet4Address && a.isSiteLocalAddress()) out.add(ni.getName() + "=" + a.getHostAddress());
                }
            }
        } catch (Exception ignored) {
        }
        return out;
    }

    /** The phone's address on its own hotspot: the one that appeared when it started. */
    private String hotspotIp() {
        String fallback = null;
        for (String e : ipv4s()) {
            String name = e.substring(0, e.indexOf('=')), ip = e.substring(e.indexOf('=') + 1);
            if (!before.contains(e)) return ip;
            if (name.startsWith("ap") || name.startsWith("swlan") || name.equals("wlan1")) fallback = ip;
        }
        return fallback;
    }

    private void emit(String s, String ssid, String pass, String message) {
        emit(s, ssid, pass, message, null);
    }

    private void emit(String s, String ssid, String pass, String message, String ip) {
        try {
            JSONObject o = new JSONObject();
            o.put("state", s);
            if (ssid != null) o.put("ssid", ssid);
            if (pass != null) o.put("password", pass);
            if (ip != null) o.put("ip", ip);
            o.put("port", MainActivity.PORT);
            if (message != null) o.put("message", message);
            state = o.toString();
        } catch (Exception e) {
            state = "{\"state\":\"" + s + "\"}";
        }
        listener.onState(state);
    }

    @SuppressWarnings("deprecation")
    void start() {
        if (!supported()) {
            emit("error", null, null, "Hotspot needs Android 8 or newer.");
            return;
        }
        if (reservation != null) {
            listener.onState(state);
            return;
        }
        WifiManager wm = (WifiManager) ctx.getSystemService(Context.WIFI_SERVICE);
        before = ipv4s();
        try {
            wm.startLocalOnlyHotspot(new WifiManager.LocalOnlyHotspotCallback() {
                @Override
                public void onStarted(WifiManager.LocalOnlyHotspotReservation r) {
                    reservation = r;
                    String ssid = null, pass = null;
                    if (Build.VERSION.SDK_INT >= 30) {
                        SoftApConfiguration c = r.getSoftApConfiguration();
                        ssid = c.getSsid();
                        pass = c.getPassphrase();
                    } else {
                        WifiConfiguration c = r.getWifiConfiguration();
                        if (c != null) {
                            ssid = c.SSID;
                            pass = c.preSharedKey;
                        }
                    }
                    if (ssid != null && ssid.length() > 1 && ssid.startsWith("\"") && ssid.endsWith("\"")) {
                        ssid = ssid.substring(1, ssid.length() - 1);
                    }
                    final String fs = ssid, fp = pass;
                    emit("starting", null, null, null);
                    // The hotspot's address takes a moment to appear.
                    final Handler h = new Handler(Looper.getMainLooper());
                    h.postDelayed(new Runnable() {
                        int tries = 0;

                        @Override
                        public void run() {
                            String ip = hotspotIp();
                            if (ip == null && ++tries < 10) {
                                h.postDelayed(this, 400);
                                return;
                            }
                            emit("on", fs, fp, null, ip);
                        }
                    }, 500);
                }

                @Override
                public void onStopped() {
                    reservation = null;
                    emit("off", null, null, null);
                }

                @Override
                public void onFailed(int reason) {
                    reservation = null;
                    String msg;
                    switch (reason) {
                        case ERROR_TETHERING_DISALLOWED:
                            msg = "This phone doesn't allow hotspots (blocked by the device policy).";
                            break;
                        case ERROR_INCOMPATIBLE_MODE:
                            msg = "Turn off the phone's regular mobile hotspot first, then try again.";
                            break;
                        case ERROR_NO_CHANNEL:
                            msg = "No free Wi-Fi channel. Try again in a moment.";
                            break;
                        default:
                            msg = "Couldn't start the hotspot. Make sure Wi-Fi and Location are turned on.";
                    }
                    emit("error", null, null, msg);
                }
            }, new Handler(Looper.getMainLooper()));
        } catch (SecurityException e) {
            emit("error", null, null, "Ferry needs the Nearby devices / Location permission to start a hotspot.");
        } catch (Exception e) {
            emit("error", null, null, "Couldn't start the hotspot: " + e.getMessage());
        }
    }

    void stop() {
        if (reservation != null) {
            reservation.close();
            reservation = null;
        }
        emit("off", null, null, null);
    }
}
