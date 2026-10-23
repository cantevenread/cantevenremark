# cantevenReMark

**Turn slides, documents, spreadsheets and pictures into clean PDFs, then send them to a reMarkable tablet. All in three steps.**

Version 2.3 · macOS 12+ · written in Go · no Chromium, no Electron

```
  ●  ○  ○          ○  ●  ○          ○  ○  ●
 Add files   →   Pick a style   →   Send to reMarkable
```

---

## Contents
1. [What it does](#what-it-does)
2. [Install the app](#install-the-app)
3. [Using it](#using-it)
4. [Sending to a reMarkable](#sending-to-a-remarkable)
5. [Options](#options)
6. [Command line](#command-line)
7. [Building from source](#building-from-source)
8. [How it works](#how-it-works)
9. [Project layout](#project-layout)
10. [Data, privacy and storage](#data-privacy-and-storage)
11. [Troubleshooting](#troubleshooting)
12. [Changelog](#changelog)

---

## What it does

| You drop in… | It's converted by… |
|---|---|
| `.pptx` `.ppt` `.key` and other slide decks | Keynote |
| `.docx` `.doc` `.pages` `.rtf` `.txt` `.md` `.html` `.odt` | Pages |
| `.xlsx` `.xls` `.csv` `.numbers` | Numbers |
| `.png` `.jpg` `.heic` `.tiff` `.gif` `.webp` | macOS `sips` |
| `.pdf` | nothing, it's passed through untouched |
| `.odp` `.ods` and anything else | LibreOffice (only if it's installed) |

Optional extras, added after conversion:

- **Writing space:** extra room below or beside each page, or a page reshaped to the reMarkable's 3:4 screen. The space can be blank, lined, grid or dotted.
- **Slide layouts:** one slide per page, slides with speaker notes, or handouts with 2, 3, 4 or 6 slides per page. You can also turn each animation step into its own page and include hidden slides.
- **Page numbers**, **smaller file size**, **combine everything into one PDF**, and a **name prefix** such as a course code.

Then it either **sends the PDFs to your reMarkable** (over Wi-Fi or a USB cable) or **saves them to your Mac**.

---

## Install the app

1. Unzip the app download (`cantevenremark-v2.3.zip`, not this source zip).
2. Open **Terminal** and run:
   ```
   xattr -cr ~/Downloads/cantevenremark-v2.3 && bash ~/Downloads/cantevenremark-v2.3/install.command
   ```
   If you unzipped it somewhere else, drag the folder into Terminal to get its path.
3. The first time you convert something, macOS asks to let it control **Keynote / Pages / Numbers**. Click **Allow**.

> **Why the Terminal step?** The app isn't signed by Apple (that needs a paid developer account), so macOS blocks it. The installer removes the block and signs the app for your Mac only.

---

## Using it

1. **Add files.** Drag files or whole folders anywhere onto the window, click the **+**, or press **⌘O**. You can also drop files onto the Dock icon or use *Open With → cantevenReMark*. Files that can't be converted are flagged before anything happens.
   - **If everything you added is already a PDF:** press **Send ▸** and they go straight to the tablet. Press **Style…** first if you want lines or page numbers added.
2. **Pick a style.** Choose *Write on them*, *Print them* or *Keep them as they are*. **More options** has everything else, and a live preview shows one finished page.
3. **Send.** Watch the progress bar, then send each PDF or **Send all**. You can also *Save to Mac* or *Open* any file.

**Shortcuts:** `⌘O` add files · `Enter` next step · `⌘,` Options · `⌘Q` quit

**Quick use:** turn on *Options → Quick use → Skip the style step*. After that, using the app is just *drop files → Send*.

---

## Sending to a reMarkable

### Wi-Fi (default)
1. In step 3 click **Open my.remarkable.com** and sign in. It shows an **8-letter code**.
2. Type the code into the app and press **Connect**. You only do this once.
3. From then on, **Send** puts PDFs in *My files* on your tablet the next time it syncs.

This uses the same upload as reMarkable's official *Read on reMarkable* browser extension. It is **not a published API**, so reMarkable could change it. If they move their servers, the addresses can be updated without a new app version (see [Advanced settings](#advanced-settings)).

### USB cable
1. Plug the tablet into your Mac with the USB-C cable.
2. On the tablet, turn on **Settings → Storage → USB web interface**.
3. The app finds the tablet automatically. Files go to its top-level folder.

This is an official reMarkable feature and needs no account.

### Don't send
Choose *this Mac only* in the top bar and the PDFs are just saved to your Downloads folder, or another folder you pick.

---

## Options

Open with the ⚙ gear in the top row. The top row also has quick switches for **Send to**, **Theme**, **Aa** (typed or handwritten text) and 📁 (your Downloads folder).

| Section | Setting |
|---|---|
| **reMarkable** | Send by Wi-Fi / USB / don't send · connection status and Disconnect · *Send each PDF automatically when it's ready* |
| **Quick use** | *Skip the style step* · *Save the style I pick each time* |
| **Saving to this Mac** | Folder (default `~/Downloads`) · *Also save every PDF to this folder* |
| **Look & feel** | Theme · Typed / Handwritten · Page turns: *E-ink ghosting* or *Smooth slide* · loading screen on/off |

**Themes:** Auto (Day or Night, following macOS) · Day (glossy 2000s blue sky) · Night · E-ink (paper and ink, like a reMarkable) · Classic 98 (grey bevels on a teal desktop) · Y2K (bubblegum gloss) · Terminal (green phosphor).

**E-ink ghosting:** switching steps flashes like an e-ink screen refreshing and leaves a fading ghost of the previous page.

### Advanced settings
`~/Library/Application Support/CantEvenRemark/state/settings.json` has an `advanced` section that isn't shown in the app:

```json
"advanced": {
  "rmAuthHost":   "https://webapp-prod.cloud.remarkable.engineering",
  "rmUploadHost": "https://internal.cloud.remarkable.com",
  "rmUsbAddress": "http://10.11.99.1"
}
```

Leave these empty to use the defaults. Quit the app before editing the file.

---

## Command line

The app's binary also works as a command-line converter:

```bash
APP=/Applications/cantevenReMark.app/Contents/MacOS/cantevenremark

$APP -o ~/Desktop/pdfs *.pptx *.docx                    # plain conversion
$APP -space remarkable -paper lined -page-numbers \
     -merge "Week 6" -prefix "BIO1001 – " -o out lecture*.pptx
$APP --browser                                          # open the UI in your browser instead
$APP --native                                           # force the app window
$APP --version
```

| Flag | Meaning |
|---|---|
| `-o DIR` | where PDFs go (default: current folder) |
| `-layout` | `slides` `notes` `handout2` `handout3` `handout4` `handout6` |
| `-builds` / `-skipped` | animation steps as pages / include hidden slides |
| `-space` | `none` `bottom` `right` `remarkable` |
| `-paper` | `blank` `lined` `grid` `dots` |
| `-page-numbers` `-compress` | extras |
| `-merge NAME` | also combine everything into `NAME.pdf` |
| `-prefix TEXT` | put TEXT before every file name |

---

## Building from source

**You need:** Go 1.24 or newer on a Mac. No Xcode and no cgo are needed.

```bash
cd cantevenremark-v2.3-source
go mod tidy                 # downloads pdfcpu + purego (first time only)
go test ./...               # runs the test suite
./packaging/build.sh        # → dist/cantevenReMark.app and dist/cantevenReMark.zip
```

- On a Mac, `build.sh` makes a **universal** app (Apple Silicon and Intel) and signs it for your machine.
- On Linux it builds an Apple Silicon app only and skips signing.
- To install your build: `bash packaging/install.command` from inside `dist/`, or copy the .app to Applications.

**Run it without building an app:**
```bash
go run .              # opens the native window (macOS)
go run . --browser    # opens the UI in your web browser (any OS; converting needs macOS)
```

**Redraw the icon:** `python3 packaging/make_icon.py packaging` (needs Pillow). This rewrites `AppIcon.icns`, `AppIcon-1024.png` and `ui/icon.svg`.

**Version number:** change `var version` in `main.go`. The build script copies it into `Info.plist`.

### Tests
| Test | What it checks |
|---|---|
| `TestPlanFor` | every file type routes to the right converter, with LibreOffice fallback |
| `TestEndToEnd` | upload → stage → convert with options → download → retry → settings → remove, all through the real HTTP API |
| `TestPostProcessRealPDF`, `TestWritingSpaceKeepsPattern` | writing space, lines/grid/dots, page numbers, compression and merging on real PDFs |
| `TestRMClient` | Wi-Fi pairing, token refresh, upload headers, USB upload (against fake reMarkable servers) |
| `TestSendThroughServer`, `TestConvertAndSendPDFsAsIs` | send / send all, auto-send, PDFs passed through byte-for-byte, nothing kept after a wipe |
| `TestServeDemo` | not a real test: `DEMO_PORT=47900 go test -run TestServeDemo` starts the UI with a fake converter, for design work |

---

## How it works

```
┌──────────────── cantevenReMark.app (one Go binary) ────────────────┐
│                                                                    │
│  native_darwin.go   NSWindow + system WKWebView (Safari engine),   │
│                     menus, file pickers. Called through the        │
│                     Objective-C runtime with purego, so no cgo.    │
│          │ loads http://127.0.0.1:<port> (local only, token-guarded)│
│          ▼                                                         │
│  server.go          tiny JSON API + serves ui/index.html           │
│  queue.go           staging → batches → jobs, one worker per app   │
│  convert.go         Keynote/Pages/Numbers via AppleScript,         │
│                     sips, textutil, LibreOffice                    │
│  pdfpost.go         writing space, page numbers, compress, merge   │
│                     (pdfcpu)                                       │
│  remarkable.go      Wi-Fi pairing + upload, USB web interface      │
│  config.go          paths, options, settings.json                  │
└────────────────────────────────────────────────────────────────────┘
```

- **Why a web view?** The UI is HTML/CSS, but it's drawn by macOS's built-in WebKit inside a normal Mac window. Nothing is bundled, so the app stays around 18 MB and light on memory.
- **Converting:** each Apple app handles one document at a time, so Keynote, Pages and Numbers each get their own worker queue, and images and PDFs run in parallel. Apps the converter opened itself are quit again after 20 seconds idle, but only if they have no documents open.
- **Rich then basic:** each AppleScript first tries the best export settings. If that Keynote/Pages version doesn't understand them, it retries with plain export, so it keeps working across app versions.
- **Writing space** works by enlarging each page's MediaBox and drawing the white area and its pattern in a new content stream. The original page content is never re-drawn or rasterised, so text stays sharp and selectable.
- **Security:** the local server only answers on `127.0.0.1`, checks the `Host` header (to stop DNS rebinding) and needs a random per-launch token for every action.

---

## Project layout

```
cantevenremark/
├── main.go              start-up, flags, app vs command-line mode, window fallback
├── native_darwin.go     macOS window, menus, open panels (purego / Objective-C)
├── native_other.go      stub for non-Mac builds
├── server.go            HTTP API used by the UI
├── queue.go             staging, batches, jobs, sending, saving
├── convert.go           AppleScripts + converters, file helpers
├── engines.go           file type → converter table, app detection
├── pdfpost.go           PDF extras (pdfcpu)
├── remarkable.go        reMarkable Wi-Fi + USB client
├── config.go            paths, options, settings
├── ui/
│   ├── index.html       the whole interface (HTML + CSS themes + JS)
│   └── icon.svg         pixel icon used in the UI
├── packaging/
│   ├── build.sh         builds the .app and .zip
│   ├── install.command  installs into /Applications and signs locally
│   ├── Info.plist       app metadata, file types, permissions text
│   ├── make_icon.py     draws the pixel icon
│   └── AppIcon.icns     app icon
└── *_test.go            tests
```

**Adding a theme:** in `ui/index.html`, copy one of the `:root[data-theme="…"]` blocks, give it a new name and change the colours. Then add the name to the two theme pickers in the same file and to `Settings.clean()` in `config.go`.

**Adding a file type:** add the extension to `extTable` in `engines.go`.

---

## Data, privacy and storage

- **Everything runs on your Mac.** Files are only uploaded when you send them to your own reMarkable account or tablet.
- **Converted files are not kept.** They're deleted when you quit, and again at the next launch in case the app crashed.
- **Only settings are kept:** `~/Library/Application Support/CantEvenRemark/state/`
  - `settings.json`: your choices
  - `remarkable.json`: the reMarkable pairing token (private file, `0600`). Use *Options → Disconnect* to remove it.
  - `log.txt`: a small log for troubleshooting
- PDFs you **save to your Mac** stay in the folder you chose, since those are yours.

---

## Troubleshooting

| Problem | Fix |
|---|---|
| “cantevenReMark can't be opened” | Run the install command above, or right-click the app → Open. |
| A file fails with “macOS blocked … Automation” | System Settings → Privacy & Security → Automation → cantevenReMark → turn on Keynote / Pages / Numbers, then press *Try again*. |
| “needs Keynote/Pages/Numbers” | Install the free app from the Mac App Store. |
| Wi-Fi: “that code didn't work” | Codes expire after a few minutes. Get a fresh one. |
| Wi-Fi: “connection expired” | Options → Disconnect, then connect again. |
| USB: “Looking for your reMarkable…” | Use a data USB-C cable, unlock the tablet, and turn on *USB web interface*. |
| The window didn't appear | The next launch opens in your browser automatically. Run the binary with `--native` to try the window again. |
| Anything else | Check `~/Library/Application Support/CantEvenRemark/state/log.txt`. |

---

## Changelog

**2.3**
- PDFs go straight to Send with no conversion.
- Converting and sending happen in one step when the tablet is connected.
- Quick-use mode that skips the style step.
- New pixel icon.
- Top bar: name, gear and quick settings. Bottom bar: three step dots.
- New themes: Classic 98, Y2K and Terminal.
- Converted files are no longer kept between launches.
- Advanced reMarkable address overrides in `settings.json`.
- The "ready" banner no longer re-animates.

**2.2**
- Step 3 sends to the reMarkable (Wi-Fi pairing or USB).
- Removed the badges from the bottom bar.

**2.1**
- Full-screen step-by-step wizard in a retro 2000s style.
- E-ink theme, handwritten text and e-ink ghosting page turns.

**2.0**
- Native Mac window (no Chromium).
- Writing space, page numbers, compression, merging and name prefixes.

**1.0**
- First version: drag in files, get PDFs.

---

Uses [pdfcpu](https://github.com/pdfcpu/pdfcpu) (Apache 2.0) and [purego](https://github.com/ebitengine/purego) (Apache 2.0). Not affiliated with reMarkable AS.
