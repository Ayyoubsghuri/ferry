package app.ferry;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.os.Build;

/** Starts Ferry in the background after the phone restarts, if the user turned that on. */
public class BootReceiver extends BroadcastReceiver {
    static final String PREFS = "ferry";
    static final String KEY = "startOnBoot";

    @Override
    public void onReceive(Context ctx, Intent intent) {
        if (!ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(KEY, true)) return;
        Intent i = new Intent(ctx, ServerService.class);
        try {
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i);
            else ctx.startService(i);
        } catch (Exception ignored) {
            // Some phones block background starts; Ferry starts next time it's opened.
        }
    }
}
