package app.ferry;

import android.app.Activity;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.webkit.URLUtil;
import android.widget.Toast;

import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.net.URLDecoder;
import java.security.SecureRandom;
import java.security.cert.X509Certificate;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import javax.net.ssl.HostnameVerifier;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.SSLSession;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509TrustManager;

/** Saves files the web UI asks to download into Downloads/Ferry, with a progress notification. */
final class Downloader {
    private static final String CHANNEL = "downloads";
    private static final AtomicInteger ids = new AtomicInteger(100);

    static void start(final Activity act, final String url, final String disposition, final String mime) {
        Toast.makeText(act, "Saving to Downloads…", Toast.LENGTH_SHORT).show();
        final Context ctx = act.getApplicationContext();
        new Thread(new Runnable() {
            @Override
            public void run() {
                download(ctx, act, url, disposition, mime);
            }
        }, "ferry-download").start();
    }

    private static void download(final Context ctx, final Activity act, String url, String disposition, String mime) {
        final int id = ids.incrementAndGet();
        NotificationManager nm = (NotificationManager) ctx.getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(new NotificationChannel(CHANNEL, "Downloads", NotificationManager.IMPORTANCE_LOW));
        }
        Saver saver = null;
        HttpURLConnection c = null;
        String name = "file";
        try {
            URL u = new URL(url);
            c = (HttpURLConnection) u.openConnection();
            if (c instanceof HttpsURLConnection && MainActivity.isPrivateHost(u.getHost())) {
                trustLocal((HttpsURLConnection) c);
            }
            c.setConnectTimeout(10000);
            c.setReadTimeout(60000);
            int code = c.getResponseCode();
            if (code != 200) throw new Exception("The server answered " + code);
            String cd = c.getHeaderField("Content-Disposition");
            if (cd == null) cd = disposition;
            String type = c.getContentType() != null ? c.getContentType() : mime;
            name = fileName(url, cd, type);
            long total = c.getContentLength();
            saver = Saver.open(ctx, name, type);
            InputStream in = c.getInputStream();
            byte[] buf = new byte[256 * 1024];
            long done = 0;
            long lastUpdate = 0;
            int n;
            while ((n = in.read(buf)) > 0) {
                saver.out.write(buf, 0, n);
                done += n;
                long now = System.currentTimeMillis();
                if (now - lastUpdate > 500) {
                    lastUpdate = now;
                    nm.notify(id, progress(ctx, name, done, total));
                }
            }
            in.close();
            saver.finish();
            nm.notify(id, finished(ctx, saver));
            final String shown = saver.name;
            act.runOnUiThread(new Runnable() {
                @Override
                public void run() {
                    Toast.makeText(act, "Saved " + shown + " to Downloads/Ferry", Toast.LENGTH_LONG).show();
                }
            });
        } catch (final Exception e) {
            if (saver != null) saver.abort();
            nm.cancel(id);
            act.runOnUiThread(new Runnable() {
                @Override
                public void run() {
                    Toast.makeText(act, "Couldn't save the file: " + e.getMessage(), Toast.LENGTH_LONG).show();
                }
            });
        } finally {
            if (c != null) c.disconnect();
        }
    }

    private static Notification.Builder builder(Context ctx) {
        return Build.VERSION.SDK_INT >= 26 ? new Notification.Builder(ctx, CHANNEL) : new Notification.Builder(ctx);
    }

    private static Notification progress(Context ctx, String name, long done, long total) {
        Notification.Builder b = builder(ctx)
                .setSmallIcon(android.R.drawable.stat_sys_download)
                .setContentTitle(name)
                .setOngoing(true)
                .setOnlyAlertOnce(true);
        if (total > 0) b.setProgress(1000, (int) (done * 1000 / total), false);
        else b.setProgress(0, 0, true);
        return b.build();
    }

    private static Notification finished(Context ctx, Saver s) {
        Notification.Builder b = builder(ctx)
                .setSmallIcon(android.R.drawable.stat_sys_download_done)
                .setContentTitle(s.name)
                .setContentText("Saved to Downloads/Ferry")
                .setAutoCancel(true);
        if (s.uri != null) {
            Intent open = new Intent(Intent.ACTION_VIEW).setData(s.uri)
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_ACTIVITY_NEW_TASK);
            int flags = Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0;
            b.setContentIntent(PendingIntent.getActivity(ctx, (int) System.currentTimeMillis(), open, flags));
        }
        return b.build();
    }

    static String fileName(String url, String disposition, String mime) {
        if (disposition != null) {
            Matcher m = Pattern.compile("filename\\*=UTF-8''([^;]+)", Pattern.CASE_INSENSITIVE).matcher(disposition);
            if (m.find()) {
                try {
                    return URLDecoder.decode(m.group(1).replace("+", "%2B"), "UTF-8");
                } catch (Exception ignored) {
                }
            }
            m = Pattern.compile("filename=\"([^\"]+)\"", Pattern.CASE_INSENSITIVE).matcher(disposition);
            if (m.find()) return m.group(1);
        }
        return URLUtil.guessFileName(url, disposition, mime);
    }

    /** Other Ferry devices on the LAN use self-signed certificates. */
    static void trustLocal(HttpsURLConnection c) {
        try {
            TrustManager[] all = new TrustManager[]{new X509TrustManager() {
                @Override
                public void checkClientTrusted(X509Certificate[] chain, String authType) {
                }

                @Override
                public void checkServerTrusted(X509Certificate[] chain, String authType) {
                }

                @Override
                public X509Certificate[] getAcceptedIssuers() {
                    return new X509Certificate[0];
                }
            }};
            SSLContext sc = SSLContext.getInstance("TLS");
            sc.init(null, all, new SecureRandom());
            c.setSSLSocketFactory(sc.getSocketFactory());
            c.setHostnameVerifier(new HostnameVerifier() {
                @Override
                public boolean verify(String hostname, SSLSession session) {
                    return MainActivity.isPrivateHost(hostname);
                }
            });
        } catch (Exception ignored) {
        }
    }
}
