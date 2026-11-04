package app.ferry;

import android.content.ContentResolver;
import android.content.Context;
import android.content.Intent;
import android.database.Cursor;
import android.net.Uri;
import android.os.Build;
import android.provider.OpenableColumns;
import android.util.Base64;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.IOException;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.List;

/** Files and text that another app shared to Ferry, waiting to be picked up by the web UI. */
final class SharedInbox {
    private final Context ctx;
    private final List<Uri> uris = new ArrayList<>();
    private String text;

    // Sequential reader state
    private int openIndex = -1;
    private long openOffset;
    private InputStream openStream;

    SharedInbox(Context ctx) {
        this.ctx = ctx;
    }

    synchronized boolean hasPending() {
        return !uris.isEmpty() || text != null;
    }

    @SuppressWarnings("deprecation")
    synchronized boolean take(Intent intent) {
        if (intent == null) return false;
        String action = intent.getAction();
        if (!Intent.ACTION_SEND.equals(action) && !Intent.ACTION_SEND_MULTIPLE.equals(action)) return false;
        clear();
        if (Intent.ACTION_SEND.equals(action)) {
            Uri u = Build.VERSION.SDK_INT >= 33
                    ? intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri.class)
                    : (Uri) intent.getParcelableExtra(Intent.EXTRA_STREAM);
            if (u != null) uris.add(u);
        } else {
            ArrayList<Uri> list = Build.VERSION.SDK_INT >= 33
                    ? intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri.class)
                    : intent.<Uri>getParcelableArrayListExtra(Intent.EXTRA_STREAM);
            if (list != null) uris.addAll(list);
        }
        CharSequence t = intent.getCharSequenceExtra(Intent.EXTRA_TEXT);
        if (t != null && uris.isEmpty()) text = t.toString();
        // Consume so a configuration change doesn't add the same items again.
        intent.setAction(Intent.ACTION_MAIN);
        return hasPending();
    }

    synchronized String describe() {
        JSONArray files = new JSONArray();
        ContentResolver cr = ctx.getContentResolver();
        for (Uri u : uris) {
            String name = "file";
            long size = -1;
            try (Cursor c = cr.query(u, null, null, null, null)) {
                if (c != null && c.moveToFirst()) {
                    int ni = c.getColumnIndex(OpenableColumns.DISPLAY_NAME);
                    int si = c.getColumnIndex(OpenableColumns.SIZE);
                    if (ni >= 0 && !c.isNull(ni)) name = c.getString(ni);
                    if (si >= 0 && !c.isNull(si)) size = c.getLong(si);
                }
            } catch (Exception ignored) {
            }
            try {
                JSONObject o = new JSONObject();
                o.put("name", name);
                o.put("size", size);
                String type = cr.getType(u);
                o.put("type", type == null ? "" : type);
                files.put(o);
            } catch (Exception ignored) {
            }
        }
        try {
            JSONObject out = new JSONObject();
            out.put("files", files);
            out.put("text", text == null ? "" : text);
            return out.toString();
        } catch (Exception e) {
            return "{\"files\":[],\"text\":\"\"}";
        }
    }

    /** Returns up to {@code max} bytes of shared file {@code index} as base64; empty string at the end. */
    synchronized String read(int index, long offset, int max) throws IOException {
        if (index < 0 || index >= uris.size()) return "";
        if (index != openIndex || offset != openOffset || openStream == null) {
            closeStream();
            openStream = ctx.getContentResolver().openInputStream(uris.get(index));
            if (openStream == null) throw new IOException("Can't open shared file");
            long skipped = 0;
            while (skipped < offset) {
                long s = openStream.skip(offset - skipped);
                if (s <= 0) break;
                skipped += s;
            }
            openIndex = index;
            openOffset = offset;
        }
        byte[] buf = new byte[Math.min(Math.max(max, 1), 4 * 1024 * 1024)];
        int filled = 0;
        while (filled < buf.length) {
            int n = openStream.read(buf, filled, buf.length - filled);
            if (n < 0) break;
            filled += n;
        }
        openOffset += filled;
        return Base64.encodeToString(buf, 0, filled, Base64.NO_WRAP);
    }

    private void closeStream() {
        if (openStream != null) {
            try {
                openStream.close();
            } catch (IOException ignored) {
            }
        }
        openStream = null;
        openIndex = -1;
    }

    synchronized void clear() {
        closeStream();
        uris.clear();
        text = null;
    }
}
