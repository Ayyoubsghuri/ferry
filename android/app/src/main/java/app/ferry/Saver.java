package app.ferry;

import android.content.ContentResolver;
import android.content.ContentValues;
import android.content.Context;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.provider.MediaStore;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.OutputStream;

/** Writes received files to Downloads/Ferry. */
final class Saver {
    final OutputStream out;
    final Uri uri;          // set on Android 10+
    final File file;        // set on older versions
    final String name;
    private final Context ctx;

    private Saver(Context ctx, OutputStream out, Uri uri, File file, String name) {
        this.ctx = ctx;
        this.out = out;
        this.uri = uri;
        this.file = file;
        this.name = name;
    }

    static Saver open(Context ctx, String name, String mime) throws IOException {
        name = clean(name);
        if (mime == null || mime.isEmpty()) mime = "application/octet-stream";
        if (Build.VERSION.SDK_INT >= 29) {
            ContentValues v = new ContentValues();
            v.put(MediaStore.MediaColumns.DISPLAY_NAME, name);
            v.put(MediaStore.MediaColumns.MIME_TYPE, mime);
            v.put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/Ferry");
            v.put(MediaStore.MediaColumns.IS_PENDING, 1);
            ContentResolver cr = ctx.getContentResolver();
            Uri uri = cr.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, v);
            if (uri == null) throw new IOException("Couldn't create the file in Downloads");
            OutputStream os = cr.openOutputStream(uri);
            if (os == null) throw new IOException("Couldn't open the file in Downloads");
            return new Saver(ctx, os, uri, null, name);
        }
        File dir = new File(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "Ferry");
        //noinspection ResultOfMethodCallIgnored
        dir.mkdirs();
        File f = new File(dir, name);
        int dot = name.lastIndexOf('.');
        String base = dot > 0 ? name.substring(0, dot) : name;
        String ext = dot > 0 ? name.substring(dot) : "";
        for (int i = 1; f.exists(); i++) f = new File(dir, base + " (" + i + ")" + ext);
        return new Saver(ctx, new FileOutputStream(f), null, f, f.getName());
    }

    void finish() throws IOException {
        out.close();
        if (uri != null && Build.VERSION.SDK_INT >= 29) {
            ContentValues v = new ContentValues();
            v.put(MediaStore.MediaColumns.IS_PENDING, 0);
            ctx.getContentResolver().update(uri, v, null, null);
        }
    }

    void abort() {
        try {
            out.close();
        } catch (IOException ignored) {
        }
        if (uri != null) ctx.getContentResolver().delete(uri, null, null);
        if (file != null) //noinspection ResultOfMethodCallIgnored
            file.delete();
    }

    private static String clean(String n) {
        if (n == null || n.trim().isEmpty()) n = "file";
        n = n.replaceAll("[\\\\/:*?\"<>|\\x00-\\x1f]", "_").trim();
        if (n.length() > 180) n = n.substring(n.length() - 180);
        return n;
    }
}
