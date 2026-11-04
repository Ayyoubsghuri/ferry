// Ferry — share files and text with devices on the same network using a short code.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	_ "embed"

	"ferry/internal/desktop"
	"ferry/internal/server"
	"ferry/internal/webdist"
)

var version = "3.1.0"

//go:embed assets/icon.png
var iconPNG []byte

//go:embed assets/icon.ico
var iconICO []byte

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(runCLI(os.Args[1], os.Args[2:]))
	}
	runApp(os.Args[1:])
}

// runApp starts Ferry: the desktop app (tray + window), or a headless server.
func runApp(args []string) {
	flag := flag.NewFlagSet("ferry", flag.ExitOnError)
	flag.Usage = func() { printUsage(); flag.PrintDefaults() }
	port := flag.Int("port", 0, "port to listen on (default: 80, falling back to 7070)")
	dataDir := flag.String("data", "", "folder for shares in progress (default: your app-data folder)")
	name := flag.String("name", "", "device name shown to other devices (default: computer name)")
	host := flag.String("host", "ferry", "network name, so others can open http://<host>.local")
	noMDNS := flag.Bool("no-mdns", false, "don't announce the .local network name")
	ips := flag.String("ip", "", "comma-separated LAN addresses to advertise (auto-detected by default)")
	headless := flag.Bool("headless", false, "run as a server only: no tray icon, no window")
	noOpen := flag.Bool("no-open", false, "don't open the app window on start")
	showVersion := flag.Bool("version", false, "print the version and exit")
	saveDir := flag.String("save-dir", "", `folder for files sent straight to this device (default: Downloads/Ferry; "none" = let the app save them)`)
	platform := flag.String("platform", "", "platform name shown to other devices")
	ipFile := flag.String("ip-file", "", "file listing this device's LAN addresses (kept current by the Android app)")
	noClipboard := flag.Bool("no-clipboard", false, "don't use the system clipboard")
	discPort := flag.Int("discovery-port", server.DefaultDiscoveryPort, "UDP port for finding other devices")
	discExtra := flag.String("discovery-extra", "", "extra host:port targets for discovery announcements (testing)")
	autoAccept := flag.Bool("auto-accept", false, "accept everything paired devices send, without asking (for servers)")
	noLocalOwner := flag.Bool("test-no-local-owner", false, "testing only")
	_ = flag.Parse(args)

	if *showVersion {
		fmt.Println("Ferry", version)
		return
	}
	if *dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		*dataDir = filepath.Join(base, "Ferry")
	}
	_ = os.MkdirAll(*dataDir, 0o755)

	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		*headless = true
	}
	setupLog(*dataDir, *headless)

	// Already running? Just bring up the window.
	if !*headless && *port == 0 {
		if url := runningInstance(); url != "" {
			desktop.OpenApp(url)
			return
		}
	}

	var hints []string
	for _, s := range strings.Split(*ips, ",") {
		if s = strings.TrimSpace(s); s != "" {
			hints = append(hints, s)
		}
	}

	if *saveDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			*saveDir = filepath.Join(home, "Downloads", "Ferry")
		}
	} else if *saveDir == "none" {
		*saveDir = ""
	}
	var clip server.ClipboardIO
	if !*noClipboard {
		if c := desktop.NewClipboard(); c != nil {
			clip = c
		}
	}
	var extra []string
	for _, e := range strings.Split(*discExtra, ",") {
		if e = strings.TrimSpace(e); e != "" {
			extra = append(extra, e)
		}
	}

	srv, err := server.Start(server.Config{
		DataDir:  *dataDir,
		Port:     *port,
		Name:     *name,
		Hostname: *host,
		NoMDNS:   *noMDNS,
		IPHints:  hints,
		Version:  version,
		UI:       webdist.FS(),

		Platform:       *platform,
		SaveDir:        *saveDir,
		Clipboard:      clip,
		OpenPath:       desktop.OpenPath,
		IPFile:         *ipFile,
		DiscoveryPort:  *discPort,
		DiscoveryExtra: extra,
		AutoAccept:     *autoAccept,
		NoLocalOwner:   *noLocalOwner,
		SetStartAtLogin: func() func(bool) error {
			if *headless {
				return nil
			}
			return desktop.SetStartAtLogin
		}(),
	})
	if err != nil {
		log.Fatalf("Ferry could not start: %v", err)
	}

	if *headless || !desktop.TraySupported {
		if !*headless && !*noOpen {
			desktop.OpenApp(srv.LocalURL())
		}
		fmt.Printf("\nFerry is running. Open %s on this computer.\n", srv.LocalURL())
		if u := srv.MDNSURL(); u != "" {
			fmt.Printf("Other devices: %s\n", u)
		}
		for _, u := range srv.LANURLs() {
			fmt.Printf("Other devices: %s\n", u)
		}
		fmt.Println("Press Ctrl+C to stop.")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		srv.Shutdown()
		return
	}

	if !*noOpen {
		desktop.OpenApp(srv.LocalURL())
	}
	shares := srv.LANURLs()
	if u := srv.MDNSURL(); u != "" {
		shares = append([]string{u}, shares...)
	}
	desktop.RunTray(desktop.TrayInfo{
		Icon:     iconPNG,
		IconICO:  iconICO,
		LocalURL: srv.LocalURL(),
		Shares:   shares,
		DataDir:  *dataDir,
		OnQuit:   srv.Shutdown,
	})
}

func setupLog(dataDir string, console bool) {
	f, err := os.OpenFile(filepath.Join(dataDir, "ferry.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	if console || runtime.GOOS != "windows" {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	} else {
		log.SetOutput(f) // Windows GUI build has no console
	}
}

func runningInstance() string {
	c := &http.Client{Timeout: 600 * time.Millisecond}
	for _, p := range server.DefaultPorts {
		u := fmt.Sprintf("http://127.0.0.1:%d/", p)
		resp, err := c.Get(u + "api/info")
		if err != nil {
			continue
		}
		var info struct{ App string }
		_ = json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		if info.App == "ferry" {
			return u
		}
	}
	return ""
}
