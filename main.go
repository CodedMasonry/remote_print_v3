package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
)

type Config struct {
	GmailAddress []string `json:"gmail_address"`
	AppPassword  string   `json:"app_password"`
	AllowedUsers []string `json:"allowed_users"`
	PrinterName  string   `json:"printer_name"`
	OutputDir    string   `json:"output_directory"`
	CleanupAfter bool     `json:"cleanup_after_print"`
}

var (
	configFile  = flag.String("config", "config.json", "Path to configuration file")
	printerName = flag.String("printer", "", "CUPS printer name (overrides config)")
	outputDir   = flag.String("output", "/tmp/remote_print_attachments", "Output directory for attachments")
	dryRun      = flag.Bool("dry-run", false, "Show what would be printed without actually printing")
	noCleanup   = flag.Bool("no-cleanup", false, "Don't delete files after printing")
	verbose     = flag.Bool("verbose", false, "Verbose output")
)

// supportedExtensions lists file types we can print.
// Images are handled via ImageMagick convert -> PDF.
// DOCX is handled via LibreOffice -> PDF.
var supportedExtensions = map[string]bool{
	".pdf":  true,
	".docx": true,
	".doc":  true,
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".bmp":  true,
	".tiff": true,
	".tif":  true,
	".webp": true,
}

func main() {
	flag.Parse()

	cfg, err := loadConfig(*configFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if *printerName != "" {
		cfg.PrinterName = *printerName
	}
	if *outputDir != "" {
		cfg.OutputDir = *outputDir
	}
	if *noCleanup {
		cfg.CleanupAfter = false
	}

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	if !*dryRun && !cupsPrinterExists(cfg.PrinterName) {
		log.Fatalf("CUPS printer '%s' not found. Available printers:", cfg.PrinterName)
		listCupsPrinters()
		os.Exit(1)
	}

	log.Println("Connecting to Gmail via IMAP...")

	imapClient, err := connectIMAP(cfg.GmailAddress[0], cfg.AppPassword)
	if err != nil {
		log.Fatalf("Failed to connect to Gmail: %v", err)
	}
	defer imapClient.Logout()

	log.Println("Successfully authenticated with Gmail")

	attachments, err := fetchAttachmentsIMAP(imapClient, cfg)
	if err != nil {
		log.Fatalf("Failed to fetch attachments: %v", err)
	}

	if len(attachments) == 0 {
		log.Println("No attachments found from allowed users")
		return
	}

	log.Printf("Found %d attachments to process\n", len(attachments))

	// Group attachments by email (SeqNum uniquely identifies an email in a session).
	type emailBatch struct {
		from        string
		subject     string
		seqNum      uint32
		attachments []Attachment
	}

	batchMap := make(map[uint32]*emailBatch)
	var batchOrder []uint32 // preserve arrival order

	for _, att := range attachments {
		if _, exists := batchMap[att.SeqNum]; !exists {
			batchMap[att.SeqNum] = &emailBatch{
				from:    att.From,
				subject: att.Subject,
				seqNum:  att.SeqNum,
			}
			batchOrder = append(batchOrder, att.SeqNum)
		}
		batchMap[att.SeqNum].attachments = append(batchMap[att.SeqNum].attachments, att)
	}

	totalSuccess := 0
	totalFiles := len(attachments)

	for _, seqNum := range batchOrder {
		batch := batchMap[seqNum]
		log.Printf("Processing batch from %s (subject: %q, %d file(s))\n",
			batch.from, batch.subject, len(batch.attachments))

		var printed []string // filenames successfully printed this batch
		var skipped []string // filenames skipped due to unsupported type
		var failed []string  // filenames that failed to print

		for _, att := range batch.attachments {
			log.Printf("  Processing: %s\n", att.Filename)

			ext := strings.ToLower(filepath.Ext(att.Filename))
			if !supportedExtensions[ext] {
				log.Printf("  Unsupported file type '%s' from %s — notifying sender\n", ext, att.From)
				if !*dryRun {
					if err := sendUnsupportedTypeReply(cfg, att); err != nil {
						log.Printf("  Warning: failed to send unsupported-type reply to %s: %v\n", att.From, err)
					}
				} else {
					log.Printf("  [DRY-RUN] Would notify %s that '%s' is unsupported\n", att.From, att.Filename)
				}
				skipped = append(skipped, att.Filename)
				continue
			}

			if !*dryRun {
				if err := printAttachmentCUPS(att.LocalPath, cfg.PrinterName); err != nil {
					log.Printf("  Error printing %s: %v\n", att.Filename, err)
					failed = append(failed, att.Filename)
					continue
				}
				log.Printf("  Successfully sent to printer: %s\n", att.Filename)
				printed = append(printed, att.Filename)
				totalSuccess++

				if cfg.CleanupAfter {
					if err := os.Remove(att.LocalPath); err != nil {
						log.Printf("  Warning: Failed to delete %s: %v\n", att.LocalPath, err)
					} else if *verbose {
						log.Printf("  Deleted: %s\n", att.LocalPath)
					}
				}
			} else {
				log.Printf("  [DRY-RUN] Would print: %s to printer '%s'\n", att.Filename, cfg.PrinterName)
				printed = append(printed, att.Filename)
			}
		}

		// Mark email seen and send batch confirmation once all files are processed.
		if !*dryRun {
			if err := markMessageSeen(imapClient, seqNum); err != nil {
				log.Printf("Warning: failed to mark email %d as seen: %v\n", seqNum, err)
			} else if *verbose {
				log.Printf("Marked email %d as seen\n", seqNum)
			}

			// Only send a confirmation if at least one file printed successfully.
			if len(printed) > 0 {
				if err := sendBatchConfirmation(cfg, batch.from, batch.subject, printed, skipped, failed); err != nil {
					log.Printf("Warning: failed to send batch confirmation to %s: %v\n", batch.from, err)
				}
			}
		} else if len(printed) > 0 {
			log.Printf("[DRY-RUN] Would send batch confirmation to %s for %d printed file(s)\n",
				batch.from, len(printed))
		}
	}

	if !*dryRun {
		log.Printf("Processing complete — %d/%d files printed successfully\n", totalSuccess, totalFiles)
	} else {
		log.Println("Dry-run complete")
	}
}

func connectIMAP(email, appPassword string) (*client.Client, error) {
	c, err := client.DialTLS("imap.gmail.com:993", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}
	if err := c.Login(email, appPassword); err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to login to IMAP: %w", err)
	}
	return c, nil
}

type Attachment struct {
	SeqNum    uint32
	EmailID   string
	From      string
	Subject   string
	Filename  string
	MimeType  string
	Size      int64
	LocalPath string
}

func fetchAttachmentsIMAP(c *client.Client, cfg *Config) ([]Attachment, error) {
	var attachments []Attachment

	mbox, err := c.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("failed to select INBOX: %w", err)
	}

	if *verbose {
		log.Printf("INBOX has %d messages\n", mbox.Messages)
	}

	if mbox.Messages == 0 {
		return attachments, nil
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	unseenIds, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("failed to search for unseen messages: %w", err)
	}

	if len(unseenIds) == 0 {
		if *verbose {
			log.Println("No unseen messages found")
		}
		return attachments, nil
	}

	if *verbose {
		log.Printf("Found %d unseen messages\n", len(unseenIds))
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(unseenIds...)

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchRFC822}, messages)
	}()

	for msg := range messages {
		envelope := msg.Envelope
		if envelope == nil {
			continue
		}

		from := ""
		if len(envelope.From) > 0 {
			from = envelope.From[0].Address()
		}
		subject := envelope.Subject

		if !isAllowedUser(from, cfg.AllowedUsers) {
			continue
		}

		literal := msg.GetBody(&imap.BodySectionName{})
		if literal == nil {
			continue
		}

		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(literal); err != nil {
			if *verbose {
				log.Printf("Failed to read message body: %v\n", err)
			}
			continue
		}

		mr, err := mail.CreateReader(buf)
		if err != nil {
			if *verbose {
				log.Printf("Failed to parse message: %v\n", err)
			}
			continue
		}

		atts := extractAttachmentsFromMessage(mr, msg.SeqNum, from, subject)
		attachments = append(attachments, atts...)
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch messages: %w", err)
	}

	return attachments, nil
}

func extractAttachmentsFromMessage(mr *mail.Reader, seqNum uint32, from, subject string) []Attachment {
	var attachments []Attachment
	var wordDec mime.WordDecoder

	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}

		disp := part.Header.Get("Content-Disposition")
		if disp == "" {
			continue
		}

		mediaType, params, err := mime.ParseMediaType(disp)
		if err != nil {
			continue
		}

		if mediaType != "attachment" {
			continue
		}

		filename := params["filename"]
		if filename == "" {
			contentType := part.Header.Get("Content-Type")
			if contentType != "" {
				_, typeParams, _ := mime.ParseMediaType(contentType)
				filename = typeParams["name"]
			}
		}

		// Decode RFC 2047 MIME encoded filenames (e.g. =?UTF-8?B?...?=).
		// Gmail encodes non-ASCII and long filenames this way, which causes
		// filepath.Ext to return "" and the file to be treated as unsupported.
		if decoded, err := wordDec.DecodeHeader(filename); err == nil {
			filename = decoded
		}

		if filename == "" {
			filename = "attachment"
		}

		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(part.Body); err != nil {
			if *verbose {
				log.Printf("Failed to read attachment: %v\n", err)
			}
			continue
		}

		localPath := filepath.Join(*outputDir, filename)
		if err := os.WriteFile(localPath, buf.Bytes(), 0644); err != nil {
			if *verbose {
				log.Printf("Failed to save attachment: %v\n", err)
			}
			continue
		}

		att := Attachment{
			SeqNum:    seqNum,
			EmailID:   fmt.Sprintf("%d", seqNum),
			From:      from,
			Subject:   subject,
			Filename:  filename,
			MimeType:  part.Header.Get("Content-Type"),
			Size:      int64(buf.Len()),
			LocalPath: localPath,
		}

		attachments = append(attachments, att)
		if *verbose {
			log.Printf("Extracted attachment: %s (%d bytes)\n", filename, att.Size)
		}
	}

	return attachments
}

func isAllowedUser(sender string, allowedUsers []string) bool {
	if len(allowedUsers) == 0 {
		return true
	}
	for _, allowed := range allowedUsers {
		if strings.EqualFold(sender, allowed) ||
			strings.EqualFold(sender, "<"+allowed+">") ||
			strings.HasSuffix(sender, "@"+allowed) {
			return true
		}
	}
	return false
}

func markMessageSeen(c *client.Client, seqNum uint32) error {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(seqNum)
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []any{imap.SeenFlag}
	return c.Store(seqSet, item, flags, nil)
}

// printAttachmentCUPS dispatches to the correct handler based on file type.
func printAttachmentCUPS(filePath, printerName string) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".docx", ".doc":
		return printDocxViaCUPS(filePath, printerName)
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff", ".tif", ".webp":
		return printImageViaCUPS(filePath, printerName)
	default:
		// PDF and anything else CUPS can handle natively.
		return printFileCUPS(filePath, printerName)
	}
}

// printImageViaCUPS converts an image to PDF via ImageMagick's `convert`
// and then sends it to CUPS. This avoids CUPS driver quirks with raw images
// on Raspberry Pi and gives consistent, margin-safe output.
func printImageViaCUPS(filePath, printerName string) error {
	pdfPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + "_img.pdf"

	if *verbose {
		log.Printf("Converting image to PDF via ImageMagick: %s -> %s\n", filePath, pdfPath)
	}

	// -auto-orient  : respect EXIF rotation (important for phone photos)
	// -quality 95   : preserve quality before PDF compression
	// -compress jpeg: keeps file size reasonable on the Pi
	cmd := exec.Command("convert",
		"-auto-orient",
		"-quality", "95",
		"-compress", "jpeg",
		filePath,
		pdfPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ImageMagick convert failed for %s: %w (output: %s)", filePath, err, string(output))
	}

	if *verbose {
		log.Printf("Image converted to PDF: %s\n", pdfPath)
	}

	if err := printFileCUPS(pdfPath, printerName); err != nil {
		os.Remove(pdfPath)
		return err
	}

	os.Remove(pdfPath)
	return nil
}

// printDocxViaCUPS converts DOCX to PDF via LibreOffice and then prints.
func printDocxViaCUPS(filePath, printerName string) error {
	if *verbose {
		log.Printf("Converting DOCX to PDF via LibreOffice: %s\n", filePath)
	}

	pdfPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".pdf"

	cmd := exec.Command("libreoffice",
		"--headless",
		"--convert-to", "pdf",
		"--outdir", filepath.Dir(filePath),
		filePath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		if *verbose {
			log.Printf("LibreOffice conversion failed: %s. Attempting direct print.\n", string(output))
		}
		return printFileCUPS(filePath, printerName)
	}

	if *verbose {
		log.Printf("DOCX converted to: %s\n", pdfPath)
	}

	if err := printFileCUPS(pdfPath, printerName); err != nil {
		os.Remove(pdfPath)
		return err
	}

	os.Remove(pdfPath)
	return nil
}

// printFileCUPS sends a file directly to CUPS via `lp`.
func printFileCUPS(filePath, printerName string) error {
	cmd := exec.Command("lp",
		"-d", printerName,
		"-n", "1",
		"-q", "50",
		filePath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "not found") {
			return fmt.Errorf("printer '%s' not found. Check CUPS configuration", printerName)
		}
		if strings.Contains(errMsg, "not accepting") {
			return fmt.Errorf("printer '%s' is not accepting jobs", printerName)
		}
		return fmt.Errorf("CUPS lp command failed: %w (stderr: %s)", err, errMsg)
	}

	return nil
}

// sendBatchConfirmation emails the sender a summary of what was printed once
// all files from their email have been processed.
func sendBatchConfirmation(cfg *Config, to, originalSubject string, printed, skipped, failed []string) error {
	from := cfg.GmailAddress[0]
	subject := fmt.Sprintf("Re: %s", originalSubject)

	var sb strings.Builder

	if len(printed) == 1 {
		sb.WriteString("Your file has been sent to the printer successfully:\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("All %d files from your email have been sent to the printer successfully:\n\n", len(printed)))
	}
	for _, f := range printed {
		sb.WriteString(fmt.Sprintf("  ✓ %s\n", f))
	}

	if len(skipped) > 0 {
		sb.WriteString("\nThe following file(s) were skipped due to unsupported format:\n\n")
		for _, f := range skipped {
			sb.WriteString(fmt.Sprintf("  ✗ %s\n", f))
		}
		sb.WriteString("\nSupported types: PDF, DOCX, DOC, JPG, JPEG, PNG, GIF, BMP, TIFF, WEBP\n")
	}

	if len(failed) > 0 {
		sb.WriteString("\nThe following file(s) failed to print:\n\n")
		for _, f := range failed {
			sb.WriteString(fmt.Sprintf("  ✗ %s\n", f))
		}
		sb.WriteString("\nPlease re-send the failed file(s) to try again.\n")
	}

	sb.WriteString("\n— Remote Print Server\n")

	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		sb.String()

	auth := smtp.PlainAuth("", from, cfg.AppPassword, "smtp.gmail.com")
	if err := smtp.SendMail("smtp.gmail.com:587", auth, from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}

	log.Printf("Sent batch confirmation to %s (%d printed, %d skipped, %d failed)\n",
		to, len(printed), len(skipped), len(failed))
	return nil
}

// sendUnsupportedTypeReply notifies the sender their file type cannot be printed.
func sendUnsupportedTypeReply(cfg *Config, att Attachment) error {
	from := cfg.GmailAddress[0]
	to := att.From
	subject := fmt.Sprintf("Re: %s", att.Subject)
	body := fmt.Sprintf(
		"\"%s\" is an unsupported file type and could not be printed.\n\n"+
			"Supported types: PDF, DOCX, DOC, JPG, JPEG, PNG, GIF, BMP, TIFF, WEBP\n\n"+
			"Please re-send as one of the supported formats.\n",
		att.Filename,
	)

	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body

	auth := smtp.PlainAuth("", from, cfg.AppPassword, "smtp.gmail.com")
	if err := smtp.SendMail("smtp.gmail.com:587", auth, from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}

	log.Printf("Sent unsupported-type reply to %s for file '%s'\n", to, att.Filename)
	return nil
}

func cupsPrinterExists(printerName string) bool {
	cmd := exec.Command("lpstat", "-p", "-d")
	output, err := cmd.Output()
	if err != nil {
		if *verbose {
			log.Printf("Failed to query CUPS printers: %v\n", err)
		}
		return false
	}
	return strings.Contains(string(output), printerName)
}

func listCupsPrinters() {
	cmd := exec.Command("lpstat", "-p", "-d")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to list CUPS printers: %v\n", err)
		return
	}
	fmt.Println(string(output))
}

func loadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				PrinterName:  "lp",
				OutputDir:    *outputDir,
				CleanupAfter: true,
			}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.PrinterName == "" {
		cfg.PrinterName = "lp"
	}

	return &cfg, nil
}
