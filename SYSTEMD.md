# Systemd Service and Timer Configuration

## Installation Instructions

1. Build the binary:
```bash
go build .
```

2. Copy binary to system location:
```bash
sudo cp remote_printer /usr/local/bin/
sudo chmod +x /usr/local/bin/remote_printer
```

3. Create app directory:
```bash
sudo mkdir -p /opt/remote_printer
sudo cp config.json credentials.json /opt/remote_printer/
sudo chown -R nobody:nogroup /opt/remote_printer
```

4. Copy service file:
```bash
sudo cp remote_printer.service /etc/systemd/system/
```

5. Copy timer file:
```bash
sudo cp remote_printer.timer /etc/systemd/system/
```

6. Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable remote_printer.timer
sudo systemctl start remote_printer.timer
```

## Monitor Status

```bash
# Check timer status
sudo systemctl status remote_printer.timer

# Check service logs
sudo journalctl -u remote_printer -f

# List next scheduled runs
sudo systemctl list-timers remote_printer.timer
```

## Troubleshooting

Check service logs:
```bash
sudo journalctl -u remote_printer -n 50
```

Run service manually:
```bash
sudo systemctl start remote_printer
```

Stop the timer:
```bash
sudo systemctl stop remote_printer.timer
sudo systemctl disable remote_printer.timer
```

---

# Service File: remote_printer.service

[Unit]
Description=Gmail Attachment Printer
Documentation=https://github.com/yourusername/remote_printer
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=nobody
Group=nogroup
WorkingDirectory=/opt/remote_printer
ExecStart=/usr/local/bin/remote_printer -config /opt/remote_printer/config.json -credentials /opt/remote_printer/credentials.json -mark-read
StandardOutput=journal
StandardError=journal
SyslogIdentifier=remote_printer

# Restart policy for oneshot
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target

---

# Timer File: remote_printer.timer

[Unit]
Description=Gmail Attachment Printer Timer
Documentation=https://github.com/yourusername/remote_printer
Requires=remote_printer.service

[Timer]
# Run every 15 minutes
OnBootSec=2min
OnUnitActiveSec=15min
Persistent=true

# Run at specific times (optional, comment out for interval-based)
# OnCalendar=*-*-* 08:00:00
# OnCalendar=*-*-* 12:00:00
# OnCalendar=*-*-* 16:00:00

[Install]
WantedBy=timers.target
