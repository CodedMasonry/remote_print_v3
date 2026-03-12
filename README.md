# Quick Start Guide

## 5-Minute Setup

### Step 1: Get Credentials (5 min)
1. Visit https://console.cloud.google.com/
2. Create a new project
3. Enable Gmail API (search for it in the library)
4. Create a service account and download the JSON key
5. Save as `credentials.json` in your project folder

### Step 2: Create Config
Copy the example config:
```bash
cp config.json.example config.json
```

Edit `config.json`:
- Add allowed email addresses
- Set printer IP address (find with `arp -a` on Windows or `arp -a` on Mac/Linux)
- Printer port is usually 9100

### Step 3: Run

```bash
# Install dependencies
go mod download

# Test first (dry run)
go run remote_printer.go -dry-run

# Run for real
go run remote_printer.go
```

## Compile for Continuous Use

```bash
go build -o remote_printer
./remote_printer  # Run the binary
```

## Run Periodically

### Linux/Mac (Cron)
```bash
# Every 15 minutes
*/15 * * * * /path/to/remote_printer -mark-read
```

Edit with `crontab -e`

### Windows (Task Scheduler)
1. Open Task Scheduler
2. Create Basic Task
3. Set trigger (e.g., every 15 minutes)
4. Set action: `C:\path\to\remote_printer.exe`

## Common Issues

| Issue | Solution |
|-------|----------|
| "Failed to authenticate" | Verify credentials.json is valid JSON |
| "No attachments found" | Check email addresses in config match senders |
| "Can't connect to printer" | Verify printer IP with `ping <ip>` |
| "404 error" | Make sure Gmail API is enabled in Google Cloud |

## Test Your Printer

Before running the script, verify your printer works:

```bash
# Linux/Mac: Send test document
lp -h <printer-ip> test.pdf

# Or use a utility to test port
telnet <printer-ip> 9100
```

## Next Steps

- Read the full README.md for advanced options
- Try `-dry-run` flag to see what would happen
- Use `-mark-read` to auto-mark emails after printing
- Set up cron/Task Scheduler for automatic runs
