# Remote Print Server — Raspberry Pi

Polls Gmail via IMAP, downloads attachments from allowed senders, and prints them via CUPS. Runs as a systemd oneshot on a timer.

---

## Prerequisites

- Raspberry Pi OS Bullseye+
- Go 1.21+
- CUPS with your printer configured
- Gmail account with an [App Password](https://myaccount.google.com/security/app-passwords) (requires 2-Step Verification)

---

## Setup

```bash
sudo apt-get install -y cups cups-client libreoffice-writer imagemagick
sudo usermod -aG lpadmin pi

cp config.json.example config.json
# Fill in gmail_address, app_password, allowed_users, printer_name
nano config.json

go mod download
go build -o remote_printer .
./remote_printer -dry-run -verbose
```

### ImageMagick PDF policy (required for image printing)

Debian/Ubuntu ships with PDF output disabled in ImageMagick's security policy. Edit `/etc/ImageMagick-6/policy.xml`:

```xml
<!-- Change this: -->
<policy domain="coder" rights="none" pattern="PDF" />
<!-- To: -->
<policy domain="coder" rights="read|write" pattern="PDF" />
```

---

## Config

```json
{
  "gmail_address": ["your.email@gmail.com"],
  "app_password": "xxxx xxxx xxxx xxxx",
  "allowed_users": ["sender@example.com"],
  "printer_name": "Brother_HL_L8360CDW",
  "output_directory": "/home/pi/remote_printer/attachments",
  "cleanup_after_print": true
}
```

`allowed_users` — matched case-insensitively against the envelope From address. Empty list allows all senders.

---

## Flags

| Flag          | Default                         | Description                                     |
| ------------- | ------------------------------- | ----------------------------------------------- |
| `-config`     | `config.json`                   | Config file path                                |
| `-printer`    | —                               | Override `printer_name` from config             |
| `-output`     | `/tmp/remote_print_attachments` | Attachment staging dir                          |
| `-dry-run`    | false                           | Log what would happen, don't print or mark seen |
| `-no-cleanup` | false                           | Keep files after printing                       |
| `-verbose`    | false                           | Extra logging                                   |

---

## Systemd Timer

```bash
sudo cp remote_printer.service remote_printer.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now remote_printer.timer
```

`remote_printer.service` — oneshot, runs as `pi`, `WorkingDirectory` set to install dir.
`remote_printer.timer` — fires 2 min after boot, then every 15 min (`OnUnitActiveSec=15min`).

---

## File Support

| Type                           | Handling                          |
| ------------------------------ | --------------------------------- |
| PDF                            | Direct CUPS (`lp`)                |
| DOCX / DOC                     | LibreOffice headless → PDF → CUPS |
| JPG, PNG, GIF, BMP, TIFF, WEBP | ImageMagick → PDF → CUPS          |

Unsupported types trigger an email reply to the sender.

---

## Troubleshooting

**MIME-encoded filenames** (e.g. `=?UTF-8?B?...?=`) are decoded via `mime.WordDecoder` before extension detection — this is why PNG files from Gmail work correctly.

**Image print fails** — almost always the ImageMagick PDF policy above. Check with:

```bash
convert test.png test.pdf && echo ok
```

**DOCX fails** — LibreOffice falls back to direct `lp` on conversion failure; check `libreoffice --version` is available.

**Auth errors** — App Passwords require 2-Step Verification to be active. Revoke and regenerate at [myaccount.google.com/security/app-passwords](https://myaccount.google.com/security/app-passwords).

```bash
# Useful diagnostics
lpstat -p -d                          # list printers
sudo journalctl -u remote_printer -f  # live logs
./remote_printer -dry-run -verbose    # test without printing
```
