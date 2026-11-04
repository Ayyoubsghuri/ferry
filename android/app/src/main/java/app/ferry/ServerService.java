package app.ferry;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import android.provider.Settings;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.os.Handler;
import android.os.Looper;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.ByteArrayOutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.HashSet;
import java.util.Set;

import java.io.File;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.NetworkInterface;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/** Keeps the bundled Ferry server running while the phone is sharing. */
public class ServerService extends Service {
    static final String ACTION_STOP = "app.ferry.STOP";
    private static final String CHANNEL = "server";

    private Process process;
    private volatile boolean stopping;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private WifiManager.MulticastLock multicastLock;
    private Thread watcher;

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_STOP.equals(intent.getAction())) {
            stopForegroundCompat();
            stopSelf();
            return START_NOT_STICKY;
        }
        goForeground();
        startServer();
        return START_STICKY;
    }

    private void goForeground() {
        NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel ch = new NotificationChannel(CHANNEL, "Sharing", NotificationManager.IMPORTANCE_LOW);
            ch.setDescription("Shown while Ferry is sharing on your network");
            nm.createNotificationChannel(ch);
        }
        int piFlags = Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0;
        PendingIntent open = PendingIntent.getActivity(this, 0,
                new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), piFlags);
        PendingIntent stop = PendingIntent.getService(this, 1,
                new Intent(this, ServerService.class).setAction(ACTION_STOP), piFlags);

        List<String> ips = lanAddresses();
        String where = ips.isEmpty() ? "Connect to Wi-Fi to share with other devices"
                : "Others can open http://" + ips.get(0) + ":" + MainActivity.PORT;

        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, CHANNEL) : new Notification.Builder(this);
        b.setSmallIcon(android.R.drawable.stat_sys_upload_done)
                .setContentTitle("Ferry is ready to receive")
                .setContentText(where)
                .setContentIntent(open)
                .setOngoing(true)
                .addAction(new Notification.Action.Builder(null, "Stop sharing", stop).build());
        Notification n = b.build();
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
        } else {
            startForeground(1, n);
        }
    }

    private synchronized void startServer() {
        if (process != null) return;
        stopping = false;
        acquireLocks();
        refreshAddresses(this);
        startWatcher();
        final File bin = new File(getApplicationInfo().nativeLibraryDir, "libferry.so");
        final File data = new File(getFilesDir(), "data");
        //noinspection ResultOfMethodCallIgnored
        data.mkdirs();
        new Thread(new Runnable() {
          @Override
          public void run() {
            while (!stopping) {
                try {
                    List<String> cmd = new ArrayList<>();
                    cmd.add(bin.getAbsolutePath());
                    cmd.add("-headless");
                    cmd.add("-no-mdns");
                    cmd.add("-port");
                    cmd.add(String.valueOf(MainActivity.PORT));
                    cmd.add("-data");
                    cmd.add(data.getAbsolutePath());
                    cmd.add("-name");
                    cmd.add(deviceName());
                    cmd.add("-platform");
                    cmd.add("android");
                    cmd.add("-save-dir");
                    cmd.add("none");
                    cmd.add("-no-clipboard");
                    cmd.add("-ip-file");
                    cmd.add(new File(getFilesDir(), "ips.txt").getAbsolutePath());
                    List<String> ips = lanAddresses();
                    if (!ips.isEmpty()) {
                        cmd.add("-ip");
                        cmd.add(join(ips));
                    }
                    ProcessBuilder pb = new ProcessBuilder(cmd);
                    pb.redirectErrorStream(true);
                    pb.redirectOutput(new File(getFilesDir(), "server.log"));
                    pb.environment().put("HOME", getFilesDir().getAbsolutePath());
                    pb.environment().put("TMPDIR", getCacheDir().getAbsolutePath());
                    Process p = pb.start();
                    synchronized (ServerService.this) {
                        process = p;
                    }
                    p.waitFor();
                } catch (Exception ignored) {
                }
                synchronized (ServerService.this) {
                    process = null;
                }
                if (!stopping) {
                    try {
                        Thread.sleep(1500); // crashed or the port was busy: try again
                    } catch (InterruptedException e) {
                        return;
                    }
                }
            }
          }
        }, "ferry-server").start();
    }

    private void acquireLocks() {
        try {
            if (wakeLock == null) {
                PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
                wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "Ferry:server");
                wakeLock.setReferenceCounted(false);
                wakeLock.acquire();
            }
            if (multicastLock == null) {
                WifiManager wm = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
                multicastLock = wm.createMulticastLock("Ferry:discovery");
                multicastLock.setReferenceCounted(false);
                multicastLock.acquire();
            }
            if (wifiLock == null) {
                WifiManager wm = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
                wifiLock = wm.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "Ferry:wifi");
                wifiLock.setReferenceCounted(false);
                wifiLock.acquire();
            }
        } catch (Exception ignored) {
        }
    }

    private String deviceName() {
        String n = null;
        try {
            if (Build.VERSION.SDK_INT >= 25) n = Settings.Global.getString(getContentResolver(), Settings.Global.DEVICE_NAME);
        } catch (Exception ignored) {
        }
        if (n == null || n.trim().isEmpty()) n = Build.MODEL;
        return n;
    }

    static List<String> lanAddresses() {
        List<String> out = new ArrayList<>();
        try {
            for (NetworkInterface ni : Collections.list(NetworkInterface.getNetworkInterfaces())) {
                if (!ni.isUp() || ni.isLoopback()) continue;
                for (InetAddress a : Collections.list(ni.getInetAddresses())) {
                    if (a instanceof Inet4Address && a.isSiteLocalAddress()) {
                        String ip = a.getHostAddress();
                        if (ni.getName().startsWith("wlan") || ni.getName().startsWith("ap") || ni.getName().startsWith("swlan")) {
                            out.add(0, ip);
                        } else {
                            out.add(ip);
                        }
                    }
                }
            }
        } catch (Exception ignored) {
        }
        return out;
    }

    /** Keeps the server's list of network addresses current (Wi-Fi changes, hotspot on/off). */
    static void refreshAddresses(Context ctx) {
        try {
            FileOutputStream out = new FileOutputStream(new File(ctx.getFilesDir(), "ips.txt"));
            out.write(join(lanAddresses()).getBytes("UTF-8"));
            out.close();
        } catch (Exception ignored) {
        }
    }

    private static String get(String path) throws Exception {
        HttpURLConnection c = (HttpURLConnection) new URL(MainActivity.HOME + path).openConnection();
        c.setConnectTimeout(1000);
        c.setReadTimeout(3000);
        try {
            InputStream in = c.getInputStream();
            ByteArrayOutputStream buf = new ByteArrayOutputStream();
            byte[] b = new byte[8192];
            int n;
            while ((n = in.read(b)) > 0) buf.write(b, 0, n);
            return buf.toString("UTF-8");
        } finally {
            c.disconnect();
        }
    }

    /**
     * While the app is in the background: copies clipboard text from paired devices and
     * notifies about incoming files. Also refreshes the address list.
     */
    private synchronized void startWatcher() {
        if (watcher != null) return;
        final Handler main = new Handler(Looper.getMainLooper());
        watcher = new Thread(new Runnable() {
            @Override
            public void run() {
                long clipSeq = -1;
                Set<String> notified = new HashSet<>();
                Set<String> seenMsgs = new HashSet<>();
                boolean firstMsgs = true;
                int tick = 0;
                while (!stopping) {
                    try {
                        Thread.sleep(2000);
                    } catch (InterruptedException e) {
                        return;
                    }
                    if (++tick % 3 == 0) refreshAddresses(ServerService.this);
                    try {
                        JSONObject r = new JSONObject(get("api/clipboard?since=" + Math.max(clipSeq, 0)));
                        long seq = r.getLong("seq");
                        JSONArray clips = r.getJSONArray("clips");
                        if (clipSeq >= 0 && clips.length() > 0 && !MainActivity.visible) {
                            JSONObject last = clips.getJSONObject(clips.length() - 1);
                            final String text = last.getString("text");
                            final String from = last.optString("fromName", "a paired device");
                            main.post(new Runnable() {
                                @Override
                                public void run() {
                                    try {
                                        ClipboardManager cm = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
                                        cm.setPrimaryClip(ClipData.newPlainText("Ferry", text));
                                    } catch (Exception ignored) {
                                    }
                                    notifyUser(200, "Copied from " + from, text);
                                }
                            });
                        }
                        clipSeq = seq;
                    } catch (Exception ignored) {
                    }
                    try {
                        JSONArray convs = new JSONObject(get("api/messages")).getJSONArray("conversations");
                        for (int i = 0; i < convs.length(); i++) {
                            JSONObject c = convs.getJSONObject(i);
                            JSONObject last = c.getJSONObject("last");
                            String id = last.optString("id");
                            boolean isNew = seenMsgs.add(id);
                            if (isNew && !firstMsgs && !MainActivity.visible && !last.optBoolean("out") && c.optInt("unread") > 0) {
                                notifyUser(500 + (c.optString("peerId").hashCode() & 0xff), c.optString("peerName"), last.optString("text"));
                            }
                        }
                        firstMsgs = false;
                    } catch (Exception ignored) {
                    }
                    if (MainActivity.visible) continue;
                    try {
                        JSONArray offers = new JSONObject(get("api/inbox")).getJSONArray("offers");
                        for (int i = 0; i < offers.length(); i++) {
                            JSONObject o = offers.getJSONObject(i);
                            String id = o.getString("id");
                            if ("pending".equals(o.optString("state")) && notified.add(id)) {
                                int count = o.optInt("count");
                                String what = "text".equals(o.optString("kind")) ? "some text"
                                        : count == 1 ? o.optString("summary") : count + " files";
                                notifyUser(300 + notified.size(), o.optString("fromName") + " wants to send you " + what,
                                        "Tap to open Ferry and accept");
                            }
                        }
                    } catch (Exception ignored) {
                    }
                }
            }
        }, "ferry-watch");
        watcher.start();
    }

    private void notifyUser(int id, String title, String text) {
        NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(new NotificationChannel("incoming", "Incoming", NotificationManager.IMPORTANCE_HIGH));
        }
        int piFlags = Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0;
        PendingIntent open = PendingIntent.getActivity(this, id,
                new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), piFlags);
        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, "incoming") : new Notification.Builder(this);
        b.setSmallIcon(android.R.drawable.stat_sys_download)
                .setContentTitle(title)
                .setContentText(text)
                .setContentIntent(open)
                .setAutoCancel(true);
        nm.notify(id, b.build());
    }

    private static String join(List<String> list) {
        StringBuilder sb = new StringBuilder();
        for (String s : list) {
            if (sb.length() > 0) sb.append(',');
            sb.append(s);
        }
        return sb.toString();
    }

    private void stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= 24) stopForeground(STOP_FOREGROUND_REMOVE);
        else stopForeground(true);
    }


    @Override
    public void onDestroy() {
        stopping = true;
        if (watcher != null) watcher.interrupt();
        watcher = null;
        synchronized (this) {
            if (process != null) process.destroy();
            process = null;
        }
        try {
            if (wakeLock != null) wakeLock.release();
            if (wifiLock != null) wifiLock.release();
            if (multicastLock != null) multicastLock.release();
        } catch (Exception ignored) {
        }
        super.onDestroy();
    }
}
