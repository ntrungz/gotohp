# gotohp

![demo](readme_assets/app_demo.webp)

Unofficial Google Photos Desktop GUI & CLI Client — fork of [xob0t/gotohp](https://github.com/xob0t/gotohp) extended with **Quota Reclaim** support.

> **Attribution & Fork Notice**: This repository (`ntrungz/gotohp`) is a fork of the original [`xob0t/gotohp`](https://github.com/xob0t/gotohp) project by `@xob0t` (and references mobile API protocol findings from [`xob0t/gpmc`](https://github.com/xob0t/gpmc)). All original upload, authentication, Live Photo pairing, and desktop GUI architecture belong to the upstream authors.

- Unlimited uploads (can be disabled)
- Quota reclaim: scan quota-consuming remote items, download originals locally, move to Trash (or optionally delete permanently), and re-upload without quota
- Drag-and-drop file upload interface
- Credential management
- Real-time upload progress tracking
- Configurable upload threads
- Individual files or directories uploads, with optional recursive scanning
- Skips files already present in your account
- CLI mode
- Configurable, persistent upload settings (stored in "%system config path%/gotohp/gotohp.config")  
   You can force local config by creating empty gotohp.config next to executable.

## [Download](https://github.com/xob0t/gotohp/releases/latest)

Nix users on Linux, see [nix/README.md](nix/README.md).

## CLI usage

Download the `gotohp-cli` release artifact for your platform. It runs without the GUI.

```shell
gotohp-cli upload /path/to/photos --recursive --threads 5
gotohp-cli reclaim --dry-run                         # scan quota-consuming items without modifying anything
gotohp-cli reclaim --download-dir ./reclaim-backups  # download, move to Trash, re-upload without quota (keeps local backup)
gotohp-cli reclaim --delete-permanently --limit 50   # also permanently clear trashed copy before re-upload to avoid SHA-1 dedup
gotohp-cli creds add "<oauth_token cookie value>"   # Embedded Setup sign-in, see below
gotohp-cli creds add "androidId=..."                 # or a raw credential string
gotohp-cli creds add - < token.txt                   # or read either from stdin
gotohp-cli creds set user@gmail.com
```

The CLI shares the config file with the GUI, but reads only the stored credentials from it. Everything else (proxy, quality, upload behaviour) is set by flags, so a scripted run is unaffected by preferences changed in the GUI. Pass `--config <path>` to use a different config file and `--account <email>` to upload with a specific stored account.

Run `gotohp-cli help`, `gotohp-cli upload --help`, or `gotohp-cli reclaim --help` for all commands and options.

The GUI binary runs the same CLI when given a command, e.g. `gotohp upload ...` or `gotohp reclaim ...`.

## Quota Reclaim

Scan your Google Photos library for items currently consuming storage quota, download the originals locally, move the quota-charged copies to Trash (with an opt-in option to delete permanently), and re-upload them without consuming quota (`Pixel XL` Original quality or `Pixel 2` Storage Saver quality).

### How it works

1. **Scan**: Paginates through your remote library (`GetLibraryState` / `GetLibraryPageInit`) and selects owned items with `quotaChargedBytes > 0` (skipping items already in Trash, Locked Folder, or shared/partner items).
2. **Download**: Downloads each item's original file to `<download-dir>/<mediaKey>/<fileName>` via an atomic `.part` transfer and preserves its capture timestamp.
3. **Trash / Delete**: Moves the remote copy to Google Photos Trash by default. If `--delete-permanently` is enabled, permanently deletes the trashed copy before re-uploading.
4. **Re-upload (0 quota)**: Uploads the downloaded local file with `UseQuota=false`. If re-uploading fails while in Trash-only mode, `gotohp` automatically restores the original item from Trash and keeps your local backup on disk.
5. **Restore metadata**: Best-effort restoration of `Favorite`, `Archived`, and `Caption` fields (disable with `--no-restore-metadata`).
6. **Local backup**: Keeps downloaded files in `--download-dir` by default (default `~/gotohp-reclaim`). Pass `--cleanup-local` to remove each local copy only after its re-upload succeeds.

### CLI & GUI Usage

- **GUI**: Select your account and click **Reclaim Quota** next to **Settings** → **Scan Library** → review items and options → **Reclaim**.
- **CLI**:

```shell
# 1. Inspect quota-consuming items without changing anything
gotohp-cli reclaim --dry-run

# 2. Test on 1-2 items first (Trash-only, keeps local backup)
gotohp-cli reclaim --download-dir ./reclaim-backups --limit 2

# 3. Permanently clear trashed copy before re-upload to avoid SHA-1 dedup
gotohp-cli reclaim --download-dir ./reclaim-backups --delete-permanently --threads 2

# 4. Re-upload in Storage Saver quality and delete local temp files after success
gotohp-cli reclaim --delete-permanently --saver --cleanup-local
```

### Run on Google Colab

```bash
%%bash
wget -q https://go.dev/dl/go1.27.0.linux-amd64.tar.gz && rm -rf /usr/local/go && tar -C /usr/local -xzf go1.27.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

git clone https://github.com/ntrungz/gotohp.git
cd gotohp
go build -tags cli -o gotohp-cli .

./gotohp-cli creds add "<oauth_token or androidId=...>"
./gotohp-cli reclaim --dry-run --no-tui
```

> **Important Disclaimers & Safety Notes**
> - **Fork & Upstream Credits**: Built on top of [`xob0t/gotohp`](https://github.com/xob0t/gotohp) (MIT License) with Quota Reclaim RPCs informed by [`xob0t/gpmc`](https://github.com/xob0t/gpmc). This fork is not affiliated with or endorsed by Google LLC or the upstream author.
> - **Unofficial API / Use at your own risk**: `gotohp` uses reverse-engineered Google Photos mobile endpoints. Google may change endpoints, rate limits, or quota policies at any time. Always keep independent backups of irreplaceable media.
> - **Trash & SHA-1 deduplication**: By default, `reclaim` only moves items to Google Photos Trash so you have a 60-day recovery window. However, Google Photos may treat an active Trash item with the exact same SHA-1 hash as a duplicate when re-uploading; if your test item fails/dedups against Trash, use `--delete-permanently` (irreversible on Google Photos, though your local copy in `--download-dir` is preserved unless `--cleanup-local` is set).
> - **Test small first**: Always run `--dry-run` and `--limit 1` (or `--limit 2`) to verify behavior on your account before running a full batch.
> - **Metadata & Albums**: Favorite, archive status, and captions are restored on a best-effort basis. Album memberships, shared links, and Apple Live Photo pairings from the old item are **not** automatically migrated to the newly uploaded copy.
> - **Credential security**: Never commit or share notebooks/logs containing your `oauth_token`, `androidId=...` credential string, or `gotohp.config`.

## Sign in

### Option 1 - Embedded setup

1. Open gotohp and click **Add Google account**.
2. Click **Open Google sign-in** and sign in with the account you want to add.
3. Sign in on the Google Embedded Setup page and click **I agree**. The page may keep loading forever; this is expected.
4. Open the browser developer tools. Under Application or Storage, open Cookies for `accounts.google.com`.
5. Copy only the value of the `oauth_token` cookie and paste it into gotohp.
6. Click **Connect account**.

From the CLI, open the same page yourself and run `gotohp-cli creds add "<oauth_token value>"`.

### Option 2 - ReVanced. No root required

<details>
  <summary><strong>Click to expand</strong></summary>

1. Install Google Photos ReVanced on your android device/emulator.
   - Install GmsCore [https://github.com/ReVanced/GmsCore/releases](https://github.com/ReVanced/GmsCore/releases)
   - Install patched apk [https://github.com/RookieEnough/Morphe-AutoBuilds/releases](https://github.com/RookieEnough/Morphe-AutoBuilds/releases) or patch it yourself
2. Connect the device to your PC via ADB.
3. Open the terminal on your PC and execute

   Windows

   ```cmd
   adb logcat | FINDSTR "auth%2Fphotos.native"
   ```

   Linux/Mac

   ```shell
   adb logcat | grep "auth%2Fphotos.native"
   ```

4. If you are already using ReVanced - remove Google Account from GmsCore.
5. Open Google Photos ReVanced on your device and log into your account.
6. One or more identical GmsCore logs should appear in the terminal.
7. Copy text from `androidId=` to the end of the line from any log.
8. That's it! 🎉

</details>

### Option 3 - Official apk. Root required

<details>
  <summary><strong>Click to expand</strong></summary>

1. Get a rooted android device or an emulator.
2. Connect the device to your PC via ADB.
3. Install [HTTP Toolkit](https://httptoolkit.com)
4. In HTTP Toolkit, select Intercept - `Android Device via ADB`. Filter traffic with

   ```text
   contains(https://www.googleapis.com/auth/photos.native)
   ```

   Or if you have an older version of Google Photos, try

   ```text
   contains(www.googleapis.com%2Fauth%2Fplus.photos.readwrite)
   ```

5. Open Google Photos app and login with your account.
6. A single request should appear.  
   Copy request body as text.
7. Add that credential string in gotohp.
8. If gotohp asks for a token binding key, keep the rooted device connected and click `Read from ADB`. gotohp will read the account's `lstBindingKeyAlias` from Android AccountManager and save it into the credential.

#### Troubleshooting

- **No Auth Request Intercepted**
  1. Log out of your Google account.
  2. Log in again.
  3. Try `Android App via Frida` interception method in HTTP Toolkit.
- **Token binding key not found**
  1. Make sure the same Google account is present on the connected device.
  2. Make sure root is available to ADB.

</details>

## Build

Follow official wails3 guide
[https://v3.wails.io/getting-started/installation/](https://v3.wails.io/getting-started/installation/)
