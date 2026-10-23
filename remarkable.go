package main

// Sending PDFs to a reMarkable tablet, two ways:
//
//   - Cloud (Wi-Fi): pair once with the 8-letter code from
//     my.remarkable.com/device/browser/connect, then upload with the same
//     simple endpoint the official "Read on reMarkable" browser extension uses.
//     (Not an officially documented API — it may change.)
//   - USB cable: the tablet's built-in "USB web interface"
//     (Settings → Storage → USB web interface) at http://10.11.99.1.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type RMClient struct {
	mu         sync.Mutex
	path       string // where the device token is kept (0600)
	device     string // long-lived device token
	user       string // short-lived user token
	userAt     time.Time
	http       *http.Client
	authHost   string
	uploadHost string
	usbBase    string

	usbMu   sync.Mutex
	usbSeen time.Time // last time the USB web interface answered
}

type rmStore struct {
	DeviceToken string    `json:"deviceToken"`
	PairedAt    time.Time `json:"pairedAt"`
}

func newRMClient(p Paths) *RMClient {
	c := &RMClient{
		path:       filepath.Join(p.State, "remarkable.json"),
		http:       &http.Client{Timeout: 5 * time.Minute},
		authHost:   "https://webapp-prod.cloud.remarkable.engineering",
		uploadHost: "https://internal.cloud.remarkable.com",
		usbBase:    "http://10.11.99.1",
	}
	if b, err := os.ReadFile(c.path); err == nil {
		var s rmStore
		if json.Unmarshal(b, &s) == nil {
			c.device = s.DeviceToken
		}
	}
	return c
}

func (c *RMClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.device != ""
}

var rmCodeRe = regexp.MustCompile(`^[a-z]{8}$`)

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Pair exchanges the one-time code for a device token and saves it.
func (c *RMClient) Pair(ctx context.Context, code string) error {
	code = strings.ToLower(strings.TrimSpace(code))
	if !rmCodeRe.MatchString(code) {
		return errors.New("the code should be 8 letters — get one at my.remarkable.com/device/browser/connect")
	}
	body, _ := json.Marshal(map[string]string{"code": code, "deviceDesc": "browser-chrome", "deviceID": uuid4()})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.authHost+"/token/json/2/device/new", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach reMarkable (%v)", err)
	}
	defer resp.Body.Close()
	tok, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 || len(bytes.TrimSpace(tok)) == 0 {
		if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 {
			return errors.New("that code didn't work — codes expire after a few minutes, so grab a fresh one")
		}
		return fmt.Errorf("reMarkable said %s", resp.Status)
	}
	c.mu.Lock()
	c.device, c.user = strings.TrimSpace(string(tok)), ""
	c.mu.Unlock()
	return c.save()
}

func (c *RMClient) save() error {
	c.mu.Lock()
	s := rmStore{DeviceToken: c.device, PairedAt: time.Now()}
	c.mu.Unlock()
	b, _ := json.Marshal(s)
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *RMClient) Disconnect() {
	c.mu.Lock()
	c.device, c.user = "", ""
	c.mu.Unlock()
	_ = os.Remove(c.path)
}

func (c *RMClient) userToken(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	dev, user, at := c.device, c.user, c.userAt
	c.mu.Unlock()
	if dev == "" {
		return "", errors.New("connect your reMarkable first")
	}
	if !fresh && user != "" && time.Since(at) < 20*time.Minute {
		return user, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", c.authHost+"/token/json/2/user/new", nil)
	req.Header.Set("Authorization", "Bearer "+dev)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach reMarkable (%v)", err)
	}
	defer resp.Body.Close()
	tok, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", errors.New("your reMarkable connection expired — connect again in Options")
	}
	if resp.StatusCode != 200 || len(bytes.TrimSpace(tok)) == 0 {
		return "", fmt.Errorf("reMarkable sign-in failed (%s)", resp.Status)
	}
	c.mu.Lock()
	c.user, c.userAt = strings.TrimSpace(string(tok)), time.Now()
	c.mu.Unlock()
	return c.user, nil
}

// UploadCloud sends a PDF to the reMarkable cloud; it appears on the tablet
// in "My files" the next time it syncs.
func (c *RMClient) UploadCloud(ctx context.Context, path, visibleName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]string{"file_name": visibleName})
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.userToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", c.uploadHost+"/doc/v2/files", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/pdf")
		req.Header.Set("rm-meta", base64.StdEncoding.EncodeToString(meta))
		req.Header.Set("rm-source", "RoR-Browser")
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("couldn't reach reMarkable (%v)", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			continue // user token expired — get a new one and retry once
		}
		if resp.StatusCode/100 != 2 {
			msg := strings.TrimSpace(string(body))
			if len(msg) > 120 {
				msg = msg[:120]
			}
			return fmt.Errorf("reMarkable didn't accept it (%s %s)", resp.Status, msg)
		}
		return nil
	}
	return errors.New("reMarkable sign-in failed")
}

// ---------- USB web interface ----------

func (c *RMClient) usbClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// CheckUSB pings the tablet's USB web interface.
func (c *RMClient) CheckUSB(ctx context.Context) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.usbBase+"/documents/", nil)
	resp, err := c.usbClient(1500 * time.Millisecond).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	ok := resp.StatusCode == 200
	if ok {
		c.usbMu.Lock()
		c.usbSeen = time.Now()
		c.usbMu.Unlock()
	}
	return ok
}

// USBReady reports whether the tablet answered recently (checked in the background).
func (c *RMClient) USBReady() bool {
	c.usbMu.Lock()
	defer c.usbMu.Unlock()
	return !c.usbSeen.IsZero() && time.Since(c.usbSeen) < 8*time.Second
}

// UploadUSB sends a PDF over the cable. It lands in the tablet's top-level folder.
func (c *RMClient) UploadUSB(ctx context.Context, path, fileName string) error {
	cl := c.usbClient(5 * time.Minute)
	// Uploads go to whichever folder was listed last, so list the top level first.
	req, _ := http.NewRequestWithContext(ctx, "GET", c.usbBase+"/documents/", nil)
	resp, err := cl.Do(req)
	if err != nil {
		return errors.New("can't see your reMarkable — plug it in with the USB cable and turn on Settings → Storage → USB web interface")
	}
	resp.Body.Close()

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, fileName))
	h.Set("Content-Type", "application/pdf")
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	mw.Close()

	req, _ = http.NewRequestWithContext(ctx, "POST", c.usbBase+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.usbBase)
	req.Header.Set("Referer", c.usbBase+"/")
	req.Header.Set("Accept", "*/*")
	resp, err = cl.Do(req)
	if err != nil {
		return fmt.Errorf("the upload over USB failed (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("your reMarkable refused the file (%s)", resp.Status)
	}
	c.usbMu.Lock()
	c.usbSeen = time.Now()
	c.usbMu.Unlock()
	return nil
}

// watchUSB keeps USBReady fresh while the app runs (only when USB is the chosen way).
func (c *RMClient) watchUSB(ctx context.Context, wanted func() bool) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if wanted() {
				cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				c.CheckUSB(cctx)
				cancel()
			}
		}
	}
}
