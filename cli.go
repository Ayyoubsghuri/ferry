package main

// Command-line interface: script transfers from a terminal, a server or a Raspberry Pi.
//
//	ferry serve                       run headless (no window, no tray)
//	ferry send [--to NAME] FILES...   send files/folders (and/or --text)
//	ferry receive CODE [-o DIR]       download a share by its code
//	ferry devices                     list paired and nearby devices
//	ferry inbox [accept|decline ID]   see and answer incoming requests
//	ferry clip [TEXT]                 send text (or the clipboard) to paired devices
//	ferry message NAME TEXT           send a short message
//
// Commands talk to the Ferry already running on this computer. If none is running, a
// temporary one starts inside the command and stops when it finishes.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ferry/internal/server"
	"ferry/internal/webdist"
)

func printUsage() {
	fmt.Fprint(os.Stderr, `Ferry `+version+` — send files and text to devices on your network.

Usage:
  ferry                          start the app (tray icon + window)
  ferry serve [flags]            run without a window (servers, Raspberry Pi, Steam Deck)
  ferry send [options] PATH...   send files or folders
      --to NAME                  send straight to a device (otherwise you get a code)
      --text TEXT                include text or a note
      --expire MINUTES           how long a code stays available (default 60)
  ferry receive CODE [-o DIR] [--password PW]
  ferry devices                  list paired and nearby devices
  ferry inbox                    list incoming requests
  ferry inbox accept|decline ID
  ferry clip [TEXT]              send TEXT, piped input, or the clipboard to paired devices
  ferry message NAME TEXT        send a message to a device
  ferry help

Flags for "ferry" and "ferry serve":
`)
}

func runCLI(cmd string, args []string) int {
	attachConsole()
	var err error
	switch cmd {
	case "serve", "daemon":
		runApp(append([]string{"-headless"}, args...))
		return 0
	case "send":
		err = cliSend(args)
	case "receive", "get":
		err = cliReceive(args)
	case "devices", "ls":
		err = cliDevices(args)
	case "inbox":
		err = cliInbox(args)
	case "clip", "clipboard":
		err = cliClip(args)
	case "message", "msg":
		err = cliMessage(args)
	case "help", "-h", "--help":
		printUsage()
		return 0
	case "version":
		fmt.Println("Ferry", version)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q.\n\n", cmd)
		printUsage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferry:", err)
		return 1
	}
	return 0
}

// ---------- talking to the local Ferry ----------

type client struct {
	base      string
	temporary bool
	srv       *server.Server
}

func connect() (*client, error) {
	if u := runningInstance(); u != "" {
		return &client{base: strings.TrimSuffix(u, "/")}, nil
	}
	base, _ := os.UserConfigDir()
	dir := filepath.Join(base, "Ferry")
	_ = os.MkdirAll(dir, 0o755)
	saveDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		saveDir = filepath.Join(home, "Downloads", "Ferry")
	}
	srv, err := server.Start(server.Config{
		DataDir: dir, Version: version, UI: webdist.FS(), SaveDir: saveDir,
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't start Ferry: %w", err)
	}
	fmt.Fprintln(os.Stderr, "(Ferry wasn't running, so it runs inside this command until it finishes.)")
	time.Sleep(900 * time.Millisecond) // let discovery hear from other devices
	return &client{base: strings.TrimSuffix(srv.LocalURL(), "/"), temporary: true, srv: srv}, nil
}

var httpc = &http.Client{Timeout: 0}

func (c *client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
	Online   bool   `json:"online"`
	Paired   bool   `json:"paired"`
}

func (c *client) devices() ([]device, error) {
	var d struct{ Paired, Nearby []device }
	if err := c.do("GET", "/api/devices?refresh=1", nil, &d); err != nil {
		return nil, err
	}
	return append(d.Paired, d.Nearby...), nil
}

func (c *client) findDevice(name string) (*device, error) {
	list, err := c.devices()
	if err != nil {
		return nil, err
	}
	n := strings.ToLower(strings.TrimSpace(name))
	var matches []device
	for _, d := range list {
		if !d.Online {
			continue
		}
		dn := strings.ToLower(d.Name)
		if dn == n || d.ID == name {
			return &d, nil
		}
		if strings.HasPrefix(dn, n) || strings.Contains(dn, n) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, m.Name)
		}
		return nil, fmt.Errorf("%q matches several devices: %s", name, strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no device called %q is on the network. Run `ferry devices` to see who's around", name)
}

// ---------- send ----------

type pick struct {
	path string // on disk
	name string // in the share (keeps folder structure)
	size int64
}

func collect(paths []string) ([]pick, error) {
	var out []pick
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, pick{path: p, name: filepath.Base(p), size: st.Size()})
			continue
		}
		root := filepath.Clean(p)
		top := filepath.Base(root)
		err = filepath.Walk(root, func(fp string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, fp)
			out = append(out, pick{path: fp, name: top + "/" + filepath.ToSlash(rel), size: info.Size()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

type progressReader struct {
	r     io.Reader
	done  *int64
	total int64
	last  time.Time
	label string
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	*p.done += int64(n)
	if time.Since(p.last) > 200*time.Millisecond || err == io.EOF {
		p.last = time.Now()
		fmt.Fprintf(os.Stderr, "\r%s %3d%%  %s / %s   ", p.label, *p.done*100/max64(p.total, 1), human(*p.done), human(p.total))
	}
	return n, err
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 {
			if u == "B" {
				return fmt.Sprintf("%d %s", n, u)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f TB", f)
}

func cliSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "device to send to")
	text := fs.String("text", "", "text or note to include")
	expire := fs.Int("expire", 60, "minutes the code stays available")
	_ = fs.Parse(reorder(args, map[string]bool{"to": true, "text": true, "expire": true}))
	files, err := collect(fs.Args())
	if err != nil {
		return err
	}
	if len(files) == 0 && *text == "" {
		return errors.New("give files or folders to send, or --text")
	}
	c, err := connect()
	if err != nil {
		return err
	}
	var target *device
	if *to != "" {
		if target, err = c.findDevice(*to); err != nil {
			return err
		}
	}
	type fileReq struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		Type string `json:"type"`
	}
	var reqFiles []fileReq
	var total int64
	for _, f := range files {
		reqFiles = append(reqFiles, fileReq{Name: f.name, Size: f.size, Type: mime.TypeByExtension(filepath.Ext(f.name))})
		total += f.size
	}
	host, _ := os.Hostname()
	var share struct {
		Code      string
		Owner     string
		ExpiresAt time.Time
		Files     []struct{ ID string }
	}
	if err := c.do("POST", "/api/t", map[string]any{
		"text": *text, "files": reqFiles, "expiresMinutes": *expire, "sender": host,
	}, &share); err != nil {
		return err
	}
	var done int64
	for i, f := range files {
		fh, err := os.Open(f.path)
		if err != nil {
			return err
		}
		pr := &progressReader{r: fh, done: &done, total: total, label: "Uploading"}
		req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/t/%s/f/%s?offset=0", c.base, share.Code, share.Files[i].ID), pr)
		req.ContentLength = f.size
		req.Header.Set("X-Owner", share.Owner)
		resp, err := httpc.Do(req)
		fh.Close()
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("upload of %s failed (%s)", f.name, resp.Status)
		}
	}
	if total > 0 {
		fmt.Fprintln(os.Stderr)
	}

	if target != nil {
		var out struct{ ID, State string }
		if err := c.do("POST", "/api/send", map[string]any{"code": share.Code, "owner": share.Owner, "deviceId": target.ID}, &out); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Waiting for %s to accept… (Ctrl+C to cancel)\n", target.Name)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		last := ""
		for {
			select {
			case <-stop:
				_ = c.do("POST", "/api/outbox/"+out.ID+"/cancel", map[string]any{}, nil)
				return errors.New("cancelled")
			case <-time.After(700 * time.Millisecond):
			}
			var ob struct {
				Outgoing []struct {
					ID, State, Error string
					Progress         float64
				}
			}
			if err := c.do("GET", "/api/outbox", nil, &ob); err != nil {
				continue
			}
			for _, o := range ob.Outgoing {
				if o.ID != out.ID {
					continue
				}
				switch o.State {
				case "done":
					fmt.Fprintf(os.Stderr, "\r%s received it.                \n", target.Name)
					return nil
				case "declined":
					return fmt.Errorf("%s declined", target.Name)
				case "failed":
					return fmt.Errorf("transfer failed: %s", o.Error)
				case "saving", "accepted":
					fmt.Fprintf(os.Stderr, "\r%s is receiving %3.0f%%   ", target.Name, o.Progress*100)
				default:
					if o.State != last {
						last = o.State
					}
				}
			}
		}
	}

	var info struct {
		LanUrls []string
		MdnsUrl string
	}
	_ = c.do("GET", "/api/info", nil, &info)
	fmt.Printf("Code: %s %s\n", share.Code[:3], share.Code[3:])
	if len(info.LanUrls) > 0 {
		fmt.Printf("Link: %s/r/%s\n", info.LanUrls[0], share.Code)
	}
	if !c.temporary {
		fmt.Printf("Available until %s while Ferry is running.\n", share.ExpiresAt.Local().Format("15:04"))
		return nil
	}
	fmt.Fprintln(os.Stderr, "Keep this running until it's been received (Ctrl+C to stop).")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	for {
		select {
		case <-stop:
			return nil
		case <-time.After(time.Second):
		}
		var t struct{ Downloads, Views int }
		req, _ := http.NewRequest("GET", c.base+"/api/t/"+share.Code, nil)
		req.Header.Set("X-Owner", share.Owner)
		if resp, err := httpc.Do(req); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&t)
			resp.Body.Close()
		} else {
			return errors.New("the share expired")
		}
		if t.Downloads > 0 || (len(files) == 0 && t.Views > 0) {
			fmt.Fprintln(os.Stderr, "Received.")
			return nil
		}
	}
}

// reorder lets flags come after file names ("ferry send a.txt --to Phone").
func reorder(args []string, valued map[string]bool) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if valued[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

// ---------- receive ----------

type shareMeta struct {
	Code      string
	Text      string
	Encrypted bool
	Salt      string
	Check     string
	Ready     bool
	Sender    string
	TotalSize int64
	Files     []struct {
		ID         string
		Name       string
		Size       int64
		StoredSize int64
		Received   int64
	}
}

func cliReceive(args []string) error {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	outDir := fs.String("o", ".", "folder to save into")
	password := fs.String("password", "", "password, if the share is locked")
	_ = fs.Parse(reorder(args, map[string]bool{"o": true, "password": true}))
	if fs.NArg() != 1 {
		return errors.New("usage: ferry receive CODE [-o DIR]")
	}
	code := strings.ReplaceAll(fs.Arg(0), " ", "")
	c, err := connect()
	if err != nil {
		return err
	}
	var meta shareMeta
	remote := ""
	if err := c.do("GET", "/api/t/"+code, nil, &meta); err != nil {
		var f struct{ Where, URL, Name string }
		if err := c.do("GET", "/api/find/"+code, nil, &f); err != nil {
			return err
		}
		if f.Where == "peer" {
			remote = f.URL
			fmt.Fprintf(os.Stderr, "Found on %s.\n", f.Name)
			if err := c.do("GET", "/api/remote/share?u="+url.QueryEscape(remote)+"&code="+code, nil, &meta); err != nil {
				return err
			}
		} else if err := c.do("GET", "/api/t/"+code, nil, &meta); err != nil {
			return err
		}
	}
	for !meta.Ready {
		fmt.Fprint(os.Stderr, "\rWaiting for the sender to finish uploading…")
		time.Sleep(1500 * time.Millisecond)
		p := "/api/t/" + code
		if remote != "" {
			p = "/api/remote/share?u=" + url.QueryEscape(remote) + "&code=" + code
		}
		if err := c.do("GET", p, nil, &meta); err != nil {
			return err
		}
	}
	var key []byte
	if meta.Encrypted {
		if *password == "" {
			return errors.New("this share is locked. Add --password")
		}
		salt, _ := base64.StdEncoding.DecodeString(meta.Salt)
		key, err = pbkdf2.Key(sha256.New, *password, salt, 250_000, 32)
		if err != nil {
			return err
		}
		if t, err := openSealed(key, meta.Check); err != nil || string(t) != "ferry-ok" {
			return errors.New("wrong password")
		}
	}
	if meta.Text != "" {
		t := meta.Text
		if key != nil {
			b, err := openSealed(key, t)
			if err != nil {
				return err
			}
			t = string(b)
		}
		fmt.Println(t)
	}
	var done int64
	for _, f := range meta.Files {
		dst := filepath.Join(*outDir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(*outDir)) {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		u := fmt.Sprintf("%s/api/t/%s/f/%s", c.base, code, f.ID)
		if remote != "" {
			u = fmt.Sprintf("%s/api/remote/file?u=%s&code=%s&id=%s", c.base, url.QueryEscape(remote), code, f.ID)
		}
		resp, err := httpc.Get(u)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("download of %s failed (%s)", f.Name, resp.Status)
		}
		out, err := os.Create(dst)
		if err != nil {
			resp.Body.Close()
			return err
		}
		pr := &progressReader{r: resp.Body, done: &done, total: meta.TotalSize, label: "Downloading"}
		if key != nil {
			err = decryptStream(key, pr, out)
		} else {
			_, err = io.Copy(out, pr)
		}
		resp.Body.Close()
		out.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	if len(meta.Files) > 0 {
		fmt.Fprintf(os.Stderr, "\nSaved %d file(s) to %s\n", len(meta.Files), *outDir)
	}
	peer := meta.Sender
	if peer == "" {
		peer = "Code " + code
	}
	_ = c.do("POST", "/api/history", map[string]any{
		"direction": "received", "kind": map[bool]string{true: "files", false: "text"}[len(meta.Files) > 0],
		"summary": fmt.Sprintf("%d file(s)", len(meta.Files)), "count": len(meta.Files), "size": meta.TotalSize,
		"peer": peer, "code": code, "path": *outDir,
	}, nil)
	return nil
}

func openSealed(key []byte, b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 28 {
		return nil, errors.New("corrupt data")
	}
	return gcmOpen(key, raw)
}

func gcmOpen(key, sealed []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, sealed[:12], sealed[12:], nil)
}

// decryptStream unlocks the chunked format written by the web app (1 MiB chunks).
func decryptStream(key []byte, r io.Reader, w io.Writer) error {
	buf := make([]byte, 1<<20+28)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			plain, derr := gcmOpen(key, buf[:n])
			if derr != nil {
				return errors.New("couldn't unlock the file (wrong password or damaged data)")
			}
			if _, werr := w.Write(plain); werr != nil {
				return werr
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ---------- devices, inbox, clipboard, messages ----------

func cliDevices(args []string) error {
	c, err := connect()
	if err != nil {
		return err
	}
	list, err := c.devices()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No devices yet. Open Ferry on another device on the same Wi-Fi.")
		return nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Paired && !list[j].Paired })
	fmt.Printf("%-24s %-10s %-8s %s\n", "NAME", "PLATFORM", "PAIRED", "STATUS")
	for _, d := range list {
		status := "offline"
		if d.Online {
			status = "on this network"
		}
		fmt.Printf("%-24s %-10s %-8s %s\n", d.Name, d.Platform, map[bool]string{true: "yes", false: "no"}[d.Paired], status)
	}
	return nil
}

func cliInbox(args []string) error {
	c, err := connect()
	if err != nil {
		return err
	}
	if len(args) == 2 && (args[0] == "accept" || args[0] == "decline") {
		var r struct{ State, SaveDir string }
		if err := c.do("POST", "/api/inbox/"+args[1]+"/"+args[0], map[string]any{}, &r); err != nil {
			return err
		}
		if args[0] == "accept" && r.SaveDir != "" {
			fmt.Printf("Accepted. Saving to %s\n", r.SaveDir)
		} else {
			fmt.Println(strings.Title(args[0]) + "d.")
		}
		return nil
	}
	var in struct {
		Offers []struct {
			ID, FromName, Kind, Summary, State, Text string
			Size                                     int64
			Unpaired                                 bool
		}
	}
	if err := c.do("GET", "/api/inbox", nil, &in); err != nil {
		return err
	}
	shown := 0
	for _, o := range in.Offers {
		if o.State != "pending" {
			continue
		}
		shown++
		who := o.FromName
		if o.Unpaired {
			who += " (not paired)"
		}
		fmt.Printf("%s  from %s: %s", o.ID, who, o.Summary)
		if o.Kind == "files" {
			fmt.Printf(" (%s)", human(o.Size))
		}
		fmt.Println()
	}
	if shown == 0 {
		fmt.Println("Nothing waiting.")
	} else {
		fmt.Println("\nAnswer with: ferry inbox accept ID   or   ferry inbox decline ID")
	}
	return nil
}

func cliClip(args []string) error {
	c, err := connect()
	if err != nil {
		return err
	}
	body := map[string]any{}
	if len(args) > 0 {
		body["text"] = strings.Join(args, " ")
	} else if st, _ := os.Stdin.Stat(); st != nil && st.Mode()&os.ModeCharDevice == 0 {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		body["text"] = string(b)
	} else {
		body["fromSystem"] = true
	}
	var r struct {
		Sent   []string
		Failed []struct{ Name, Error string }
	}
	if err := c.do("POST", "/api/clipboard", body, &r); err != nil {
		return err
	}
	if len(r.Sent) > 0 {
		fmt.Println("Sent to", strings.Join(r.Sent, ", "))
	}
	for _, f := range r.Failed {
		fmt.Fprintf(os.Stderr, "Not sent to %s: %s\n", f.Name, f.Error)
	}
	return nil
}

func cliMessage(args []string) error {
	if len(args) < 2 {
		return errors.New(`usage: ferry message NAME "text"`)
	}
	c, err := connect()
	if err != nil {
		return err
	}
	d, err := c.findDevice(args[0])
	if err != nil {
		return err
	}
	if err := c.do("POST", "/api/messages", map[string]any{"peerId": d.ID, "text": strings.Join(args[1:], " ")}, nil); err != nil {
		return err
	}
	fmt.Println("Sent to", d.Name)
	return nil
}
