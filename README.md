# Raspberry Pi Quick Start - 10 Minutes to Printing

---

## Prerequisites

- Raspberry Pi (any model with 512 MB+ RAM)
- Raspberry Pi OS (Bullseye or newer)
- Network printer (connected and working)
- Gmail account with 2-Step Verification enabled

---

## Step 1: SSH into Your Raspberry Pi (1 min)

```bash
# From your computer:
ssh pi@raspberrypi.local

# Or with IP address:
ssh pi@192.168.1.50
```

Password: `raspberry` (or your custom password)

---

## Step 2: Install Required Packages (3 min)

```bash
# Update system
sudo apt-get update
sudo apt-get upgrade -y

# Install dependencies
sudo apt-get install -y cups cups-client libreoffice-writer golang-go

# Add user to lpadmin group
sudo usermod -aG lpadmin pi

# Restart CUPS
sudo systemctl restart cups

# Log out and back in for group changes to take effect
exit
```

Log back in:
```bash
ssh pi@raspberrypi.local
```

---

## Step 3: Find Your Printer (1 min)

```bash
# List available printers
lpstat -p -d

# Example output:
# printer Brother_HL_L8360CDW is idle...
# device for Brother_HL_L8360CDW: ipp://192.168.1.100:631/ipp/print

# Copy the printer name (e.g., Brother_HL_L8360CDW)
```

---

## Step 4: Get Gmail App Password (1 min)

1. Go to https://myaccount.google.com/security
2. Enable **2-Step Verification** (if not already)
3. Find **App passwords** (appears after 2-Step Verification)
4. Select **Mail** and **Windows/Mac/Linux**
5. Copy the 16-character password (looks like: `abcd efgh ijkl mnop`)

---

## Step 5: Download and Configure (2 min)

```bash
# Create directory
mkdir -p ~/remote_printer
cd ~/remote_printer

# Copy the files:
# remote_printer-rpi.go
# config-rpi.json.example
# go.mod
# (paste them into this directory)

# Create config
cp config-rpi.json.example config.json

# Edit config
nano config.json
```

Edit these fields:
```json
{
  "gmail_address": ["your.email@gmail.com"],
  "app_password": "xxxx xxxx xxxx xxxx",
  "allowed_users": [
    "sender1@example.com",
    "sender2@example.com"
  ],
  "printer_name": "Brother_HL_L8360CDW",
  "output_directory": "/home/pi/remote_printer/attachments",
  "cleanup_after_print": true
}
```

Save: `Ctrl+O`, `Enter`, `Ctrl+X`

---

## Step 6: Build and Test (2 min)

```bash
# Download dependencies
go mod download

# Build
go build -o remote_printer remote_printer-rpi.go

# Test (dry-run, no printing)
./remote_printer -dry-run

# Expected output:
# Connecting to Gmail via IMAP...
# Successfully authenticated with Gmail
# INBOX has 5 messages
# Found 2 attachments to process
# [DRY-RUN] Would print: document.pdf to printer 'Brother_HL_L8360CDW'
```

If you see this, everything works! ✅

---

## Step 7: Run for Real (1 min)

```bash
# Print for real
./remote_printer

# Expected output:
# Connecting to Gmail via IMAP...
# Successfully authenticated with Gmail
# INBOX has 5 messages
# Found 2 attachments to process
# Processing: document.pdf (From: sender@example.com, Email ID: 123)
# Successfully sent to printer: document.pdf
# [Auto-deleted: document.pdf]
# Processing complete - 1/1 files printed successfully
```

---

## Step 8: Schedule to Run Automatically (1 min)

**Option A: Simple (Cron)**
```bash
# Edit crontab
crontab -e

# Choose nano (option 2)

# Add this line (runs every 15 minutes):
*/15 * * * * /home/pi/remote_printer/remote_printer >> /tmp/remote_printer.log 2>&1

# Save: Ctrl+O, Enter, Ctrl+X
```

**Option B: More Reliable (Systemd)**
```bash
# Create service file
sudo nano /etc/systemd/system/remote_printer.service
```

Paste:
```ini
[Unit]
Description=Gmail Attachment Printer
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=pi
WorkingDirectory=/home/pi/remote_printer
ExecStart=/home/pi/remote_printer/remote_printer
StandardOutput=journal
StandardError=journal
SyslogIdentifier=remote_printer

[Install]
WantedBy=multi-user.target
```

Save: `Ctrl+O`, `Enter`, `Ctrl+X`

```bash
# Create timer file
sudo nano /etc/systemd/system/remote_printer.timer
```

Paste:
```ini
[Unit]
Description=Gmail Printer Timer
Requires=remote_printer.service

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min
Persistent=true

[Install]
WantedBy=timers.target
```

Save: `Ctrl+O`, `Enter`, `Ctrl+X`

```bash
# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable remote_printer.timer
sudo systemctl start remote_printer.timer

# Check status
sudo systemctl list-timers remote_printer.timer
```

---

## Done! 🎉

Your Raspberry Pi will now:
- ✅ Check Gmail every 15 minutes
- ✅ Download PDF and DOCX attachments from allowed senders
- ✅ Print them automatically
- ✅ Delete files after printing
- ✅ Run forever with zero manual intervention

---

## Testing It Works

```bash
# Send yourself an email with a PDF attachment from an allowed sender

# Wait 15 minutes (or run manually)
/home/pi/remote_printer/remote_printer

# Check logs
tail -f /tmp/remote_printer.log          # If using cron
sudo journalctl -u remote_printer -f     # If using systemd

# Check printer queue
lpq

# Check that file was deleted
ls /home/pi/remote_printer/attachments/  # Should be empty
```

---

## Common Commands

```bash
# Run manually
~/remote_printer/remote_printer

# Test with dry-run
~/remote_printer/remote_printer -dry-run

# Run with verbose logging
~/remote_printer/remote_printer -verbose

# Don't auto-delete files
~/remote_printer/remote_printer -no-cleanup

# Check cron logs (if using cron)
tail -20 /tmp/remote_printer.log

# Check systemd logs (if using systemd)
sudo journalctl -u remote_printer -n 20

# See live logs
sudo journalctl -u remote_printer -f

# Check printer status
lpstat -o                   # See print queue
lpstat -p -d                # List printers
lpq                         # Quick queue view

# Remove printer (if needed)
sudo lpadmin -x Brother_HL_L8360CDW
```

---

## Troubleshooting

### Error: "printer not found"
```bash
# Check printer name
lpstat -p -d

# Update config.json with correct name
nano ~/remote_printer/config.json
```

### Error: "Failed to authenticate"
- Double-check Gmail address in config.json
- Double-check app password (16 chars with spaces)
- Go to https://myaccount.google.com/security/app-passwords
- Delete old password and generate new one

### DOCX files not converting
```bash
# Check LibreOffice
libreoffice --version

# If not installed:
sudo apt-get install -y libreoffice-writer
```

### Files not printing
```bash
# Test printer manually
echo "Test" | lp -d Brother_HL_L8360CDW

# Check if printer accepting jobs
lpstat -p -d

# Restart CUPS
sudo systemctl restart cups
```

### Disk space filling up
```bash
# Check size
du -sh ~/remote_printer/

# Ensure cleanup_after_print is true in config.json
cat ~/remote_printer/config.json | grep cleanup

# Manual cleanup
rm -f ~/remote_printer/attachments/*
```

---

## Customization

### Print different times
Edit crontab:
```bash
crontab -e

# Examples:
0 9 * * * /home/pi/remote_printer/remote_printer    # 9 AM daily
0 */2 * * * /home/pi/remote_printer/remote_printer  # Every 2 hours
*/30 * * * * /home/pi/remote_printer/remote_printer # Every 30 minutes
```

### Different paper size or sides
Edit `remote_printer-rpi.go`, find `printFileCUPS()`:
```go
cmd := exec.Command("lp",
    "-d", printerName,
    "-o", "sides=two-sided-long-edge",  // Add this
    "-o", "media=A4",                   // And this
    filePath,
)
```

Rebuild:
```bash
cd ~/remote_printer
go build -o remote_printer remote_printer-rpi.go
```

---

## Next Steps

Once working, you can:

1. **Add more allowed users** - Edit config.json
2. **Change print schedule** - Edit crontab or systemd timer
3. **Monitor from phone** - Install CUPS mobile app
4. **Set up alerts** - Log to syslog or email errors
5. **Automate more** - Add other email tasks

---

## You're Done! 🚀

Your Raspberry Pi is now an automated printing server. No more manual work—just send emails and the Pi handles the rest!

For help:
- Check logs: `sudo journalctl -u remote_printer -f`
- Read full guide: `SETUP-RPI.md`
- See optimizations: `RPI-OPTIMIZATIONS.md`
- Compare versions: `COMPARISON.md`

Happy printing! 🖨️
