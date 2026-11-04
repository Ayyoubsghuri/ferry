# Ferry

Send files, folders, text and messages between your phone and computers on the same Wi‑Fi. No account, no cloud, no internet needed, and no certificate warnings.

Ferry runs as a desktop app (Windows, macOS, Linux), an Android app, a command-line tool, and works in any browser on the network.

## How it works

1. **Pick files** (or a whole folder), add a note if you like, or just type some text.
2. **Choose a device** from the list. Every device with Ferry open on the same Wi‑Fi shows up automatically, and so does any phone or computer that has this device's address open in a browser (no app needed).
3. **The other device asks Accept or Refuse.** You see "Waiting…", then "Receiving…", then "Received it" (or "Refused"). Requests wait until they're answered, and you can cancel while waiting.

Computers save to `Downloads/Ferry` (folder structure kept); phones and browsers save to Downloads.

**Pairing is optional.** Pair the devices you use often (Devices → Show pairing PIN on one, Pair on the other). Paired devices are verified with key fingerprints, can be set to *Accept files without asking*, and can share the clipboard.

## Features

- **Instant discovery** over UDP broadcast, with a subnet scan as fallback. Nothing to type.
- **Codes, as an option.** On Send, *Get a code instead* gives a 6‑digit code valid for an hour. On the other device, type it under **Receive** to download — Ferry also looks on other devices on the Wi‑Fi for it.
- **Clipboard sync.** On computers, anything you copy can sync to paired devices within a second, and received text lands straight in the clipboard. On Android, received text is copied even with the app in the background. Android doesn't let apps read the clipboard in the background, so sending from the phone is one tap.
- **Messages.** A small chat thread with any nearby device, for context like "logs attached, check line 40". Notes typed with a transfer are shown in the accept prompt.
- **Preview before accepting.** In the Accept / Refuse prompt, play videos and music, view photos, PDFs and text, or list what's inside a .zip, streamed from the sender.
- **Light by default**, with a dark mode switch (moon icon at the top) that's remembered.
- **Always ready.** Android keeps a small background service running, optionally starts with the phone, and asks to be exempted from battery restrictions. Desktop apps live in the tray and can start at login.
- **Privacy on shared Wi‑Fi.** Turn off *Allow requests from unpaired devices* so only paired devices can send or message you, and *Ask other browsers for a PIN* so nobody else can open your device. Wrong guesses are rate‑limited.
- **History** of everything sent and received (files, text, clipboard), with sizes, times, status ("Received by Pixel 8") and shortcuts to the files.
- **No Wi‑Fi? Send over a hotspot (Android).** On the sending phone choose *No Wi‑Fi? Send over a hotspot*: it starts a hotspot with a random name and shows a QR code. On the other phone open Ferry → Receive → *Scan the sender's code*; it joins the hotspot by itself and appears in the sender's device list, then accepts the file as usual. A computer can join too using the name and password shown under the QR. Works phone → phone, phone → PC, and PC → phone (the PC joins the phone's hotspot).
- **Windows app window** has Ferry's own title bar (minimize / maximize / close built into the header, drag anywhere empty) instead of the standard Windows frame. Needs the WebView2 runtime that ships with Windows 10/11; without it Ferry opens in an Edge app window.
- Multiple files and whole folders, resumable uploads, no size limit, paste with Ctrl+V, drag and drop, "Share to Ferry" from Android's share menu.

## Command line

The same `ferry` program is the CLI. Commands use the Ferry already running on the computer; if none is running, a temporary one runs inside the command.

```bash
ferry serve                              # run without a window (servers, Raspberry Pi, Steam Deck)
ferry serve -auto-accept                 #   …and accept everything paired devices send
ferry devices                            # who's around
ferry send report.pdf photos/ --to "Pixel" --text "check page 4"
ferry send notes.txt                     # without --to: prints a code another Ferry can use with "ferry receive"
ferry receive 482913 -o ~/Downloads      # add --password for locked shares
ferry inbox                              # incoming requests
ferry inbox accept 84a2aa6c
echo "some text" | ferry clip            # send text to paired devices (no argument = clipboard)
ferry message Pixel "on my way"
```

On Windows, run `Ferry-3.2.0-windows-x64.exe <command>` from a terminal (rename it to `ferry.exe` for convenience).

## Install

| Platform | File | Notes |
|---|---|---|
| Android 7+ | `Ferry-3.2.0-android.apk` | Allow "install unknown apps" for your browser or file manager. |
| Windows 10/11 | `Ferry-3.2.0-windows-x64.exe` | Single file. SmartScreen may warn because it isn't code‑signed ("More info → Run anyway"). Allow it through the firewall on **private** networks. |
| Linux | `Ferry-3.2.0-linux-x64.tar.gz` | Extract and run `./install.sh`, or just `./ferry`. |
| macOS | built by the GitHub workflow | Needs a Mac to build the menu‑bar version (see below). |

### Network
- A browser on the same computer as Ferry gets the full app; browsers on other devices get the send/receive screen and appear in the device list.
- Browsers use plain `http://`: `http://192.168.1.5` when port 80 is free, otherwise `:7070` (the Android app always uses 7070). `http://ferry.local` works on most computers and iPhones.
- Ferry devices talk to each other over HTTPS on the same port, verified with pinned keys once paired.
- UDP 47321 is used for discovery. Allow Ferry through the firewall on private networks.
- On Linux, port 80 needs `sudo setcap cap_net_bind_service=+ep ~/.local/bin/ferry`; otherwise Ferry uses 7070.

## Project layout

```
main.go, cli.go             entry point: app, headless server, command-line tool
internal/server/            Go backend: API, storage, TLS, discovery, pairing, inbox, chat, history
internal/desktop/           tray icon, app window, clipboard, start at login
internal/webdist/dist/      built web UI (embedded into the binary)
webui/                      React + Vite source for the UI
android/                    Android app (WebView shell running the Go server as a background service)
packaging/, scripts/        desktop packaging and build script
.github/workflows/build.yml builds every platform; tag v* to publish a release
```

## Building

**Everything at once:** push to GitHub. The workflow builds Windows, Linux, macOS and the Android APK as artifacts; push a tag like `v3.0.1` to publish a release with all files.

**Locally:**
```bash
go mod tidy
scripts/build.sh            # → dist/ (Windows + Linux); needs Go 1.24+, Node 18+, rsrc (go install github.com/akavel/rsrc@latest)
scripts/build.sh macos      # on a Mac
cd webui && npm install && npm run build && cd .. && go run . -port 7070    # run from source
```
Android: build the server binaries into `android/app/src/main/jniLibs/` (see the workflow), then `cd android && ./gradlew assembleRelease`, or open `android/` in Android Studio.

### Flags for `ferry` / `ferry serve`
```
-port N           port (default 80, then 7070)
-name NAME        device name others see (default: computer name)
-host NAME        network name, default "ferry" → http://ferry.local
-save-dir DIR     where files sent straight to this device go (default ~/Downloads/Ferry)
-auto-accept      accept everything from paired devices without asking
-headless         no tray or window (same as "ferry serve")
-no-open          start in the tray without opening a window
-no-mdns          don't announce the .local name
-no-clipboard     don't touch the system clipboard
-data DIR         where Ferry keeps its state
```

### Signing key
`android/keystore/ferry.jks` (password `ferryapp`) signs every APK so updates install over each other. **Replace it with your own private key before publishing anywhere public.** Ferry is a new app id (`app.ferry`), so it installs alongside the old SendLocal app; uninstall that one.

## License
MIT
