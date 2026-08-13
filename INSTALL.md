# Installing Keel

Keel is two pieces that work together:

- a **browser extension**, which reads the recommendations already on your screen
- a **desktop app**, which stores them on your computer

Both have to be installed. The extension on its own has nowhere to put anything.

Keel is not in the Chrome Web Store yet, so it is installed by hand. There are
two routes: download a prebuilt desktop app, or build it yourself in about ten
minutes. Either way you do not need to know how to program — every command below
is copy-and-paste.

**Nothing you record leaves your computer.** Not to us, not to anyone. There is
no account and nothing to sign up for.

---

## The short way — no Go needed

If a release is available, download the desktop app instead of building it:

1. Go to [Releases](https://github.com/larslarsen/Keel/releases) and download
   the file for your machine:
   - Windows — `keel-host-windows-amd64.exe`
   - Mac (Apple silicon, 2020 onward) — `keel-host-darwin-arm64`
   - Mac (Intel) — `keel-host-darwin-amd64`
2. Download `keel-extension.zip` from the same page and unzip it somewhere you
   will keep — that folder is the extension.

   **Updating an existing folder:** when Windows asks what to do about files
   that are already there, choose **Replace the files in the destination**. If
   you choose *Skip*, the old files stay and you end up with half of one build
   and half of another — the extension then fails with a message naming
   `lib/protocol.js`. The safe alternative is to delete the folder first
   (click it in File Explorer and press Delete, or run
   `rd /s /q "%USERPROFILE%\Downloads\keel-extension"`) and extract fresh.
3. Put the desktop app next to the unzipped folder and install it:
   - **Windows — just double-click `keel-host-windows-amd64.exe`.** No terminal.
     Running it with no arguments *is* the install. A window may flash and
     close; that is expected, and the result is written to a file (below).
   - **Mac** — open a terminal in that folder and run it with `install`
     (Step 3 below has the exact command).
4. Load the extension folder as described in Step 4.

**Windows leaves a report.** Every install writes `install-report.txt` next to
the app. Open it in Notepad: the last line says `RESULT: SUCCESS` or
`RESULT: FAILED`, and a failure names the first thing that went wrong. It holds
file paths and browser settings keys only — nothing you have watched or
searched — so it is safe to attach to an issue.

**Your computer will warn you about these files, and it also marks them.**
They are not code-signed — a certificate costs money this project has not
spent, and saying so is better than letting you meet the warning unwarned.

Windows records that a file came from the internet in a hidden "Zone.Identifier"
mark. A marked program is restricted, and a browser starting Keel's desktop app
in the background gets nothing at all — it reports the app as missing, exactly
as if nothing were installed. This is why building from source works instantly
while the identical downloaded file appears not to work.

**`keel-host.exe install` clears the mark from itself and from the extension
folder,** so running the installer once is enough. If you want to do it by hand
first: right-click the `.exe` and the `.zip` → *Properties* → tick **Unblock** →
OK, and extract the zip *after* unblocking it.

- **Windows:** SmartScreen says "Windows protected your PC". Click *More info*,
  then *Run anyway*.
- **Mac:** the file will not open. Right-click it, choose *Open*, then confirm.
  Or run `xattr -d com.apple.quarantine keel-host-darwin-arm64` in Terminal.

If that trade is not one you want to make, build it yourself instead — it is the
only way to check the program against the source, and it is what the rest of
this page describes.

---

## Building it yourself

You need two free tools and a browser.

| | |
|---|---|
| **Go** | Builds the desktop app. Download from <https://go.dev/dl/> and run the installer. |
| **Git** | Downloads the code. <https://git-scm.com/downloads> — on macOS you may already have it. |
| **Browser** | Chrome, Brave, or Edge. |

After installing Go, **close and reopen your terminal** so it notices the new
command. Then check both are working:

```
go version
git --version
```

Each should print a version number. If either says "not found", the installer
did not finish or the terminal is still the old one.

---

## Step 1 — Get the code

**Windows** (open *PowerShell* from the Start menu):

```powershell
cd $HOME
git clone https://github.com/larslarsen/Keel.git
cd Keel
```

**macOS / Linux** (open *Terminal*):

```bash
cd ~
git clone https://github.com/larslarsen/Keel.git
cd Keel
```

---

## Step 2 — Build the desktop app

**Windows:**

```powershell
cd daemon
go build -o keel-host.exe .
```

**macOS / Linux:**

```bash
cd daemon
go build -o keel-host .
```

The first build downloads dependencies and takes a minute or two. Later builds
are seconds. When it finishes it prints nothing at all — that is success.

---

## Step 3 — Connect it to your browser

**Windows:** double-click `keel-host.exe` in File Explorer, or from PowerShell:

```powershell
.\keel-host.exe install
```

Both do the same thing. On Windows the result is also written to
`install-report.txt` beside the app, so you do not have to read the console.

**macOS / Linux:**

```bash
./keel-host install
```

This does two things: it tells your browsers where the desktop app is, and it
prepares the extension folder so it is ready to load in the next step. Run it
before Step 4.

It prints which browsers it found, for example:

```
installed Brave     /Users/you/Library/Application Support/...
installed Chrome    /Users/you/Library/Application Support/...
skipped:  Chromium, Edge, Firefox
prepared  /Users/you/Keel/extension/manifest.json
```

"Skipped" just means that browser is not installed. You do not need to give it
any extension ID — Keel's is fixed.

---

## Step 4 — Load the extension

1. Open your browser and go to:
   - Chrome — `chrome://extensions`
   - Brave — `brave://extensions`
   - Edge — `edge://extensions`
2. Turn on **Developer mode** (top right).
3. Click **Load unpacked**.
4. Select the **`extension`** folder inside the `Keel` folder you downloaded.
   Select the folder itself — do not go inside it.

Keel appears in your extensions list.

---

## Step 5 — Check it works

1. Click the puzzle-piece icon in your toolbar and pin **Keel** so you can see
   it.
2. Open any YouTube video.
3. Click the Keel icon.

The panel should open and say **"Desktop app connected"**, then fill with the
videos YouTube is recommending as you browse.

It starts empty. Watch a few videos and it fills up.

---

## Keeping it running

The desktop app starts automatically when your browser needs it. There is no
program to launch and nothing sits in your menu bar or system tray.

**Your recordings are in one file:**

- Windows — `%APPDATA%\keel\keel.sqlite`
- macOS — `~/Library/Application Support/keel/keel.sqlite`
- Linux — `~/.config/keel/keel.sqlite`

---

## Updating

From the `Keel` folder:

```bash
git pull
cd daemon
go build -o keel-host .        # keel-host.exe on Windows
```

Then go to your browser's extensions page and click **Reload** under Keel.

Your recordings are kept — updating never touches them.

---

## Removing it

1. Extensions page → **Remove** under Keel.
2. In the `Keel/daemon` folder, run `./keel-host uninstall` (`.\keel-host.exe
   uninstall` on Windows).
3. Delete the database file listed above if you want your recordings gone too.

---

## If something goes wrong

**The panel says the desktop app is not connected.**
Run the install command from Step 3 again (on Windows, double-click the app),
then reload the extension from the extensions page. The browser only looks for
the desktop app when the extension loads.

On Windows, open `install-report.txt` beside the app first. If it ends in
`RESULT: FAILED`, the line beginning `First error:` says what to fix. If it ends
in `RESULT: SUCCESS` and the panel is still not connected, attach that file to
an issue — the registration itself is verified, so the problem is elsewhere.

**"Load unpacked" says the manifest is missing or invalid.**
Step 3 was skipped, or was run from somewhere other than the `daemon` folder
inside the repository — that is what prepares the extension. Run it again from
there and look for the "prepared" line.

**"go: command not found" or "git is not recognized".**
The terminal was open before you installed them. Close it, open a new one, and
try again.

**macOS refuses to run the app.**
You built it yourself, so this is unusual — but if it happens, run
`xattr -d com.apple.quarantine keel-host` in the `daemon` folder.

**The panel opens but stays empty.**
It only records on watch pages and the YouTube homepage. Open a video and give
it a few seconds. If it is still empty, reload the extension.

**Nothing above helped.**
Open an issue at <https://github.com/larslarsen/Keel/issues> and say which
browser and operating system you are on. Paste anything red from the extensions
page under Keel → "Errors".
