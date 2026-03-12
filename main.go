package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"mime"
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
	PrinterName  string   `json:"printer_name"` // CUPS printer name
	OutputDir    string   `json:"output_directory"`
	CleanupAfter bool     `json:"cleanup_after_print"` // Delete files after printing
}

var (
	configFile  = flag.String("config", "config.json", "Path to configuration file")
	printerName = flag.String("printer", "", "CUPS printer name (overrides config)")
	outputDir   = flag.String("output", "/tmp/remote_print_attachments", "Output directory for attachments")
	dryRun      = flag.Bool("dry-run", false, "Show what would be printed without actually printing")
	noCleanup   = flag.Bool("no-cleanup", false, "Don't delete files after printing")
	verbose     = flag.Bool("verbose", false, "Verbose output")
)

func main() {
	flag.Parse()

	// Load configuration
	cfg, err := loadConfig(*configFile)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Override with command line flags
	if *printerName != "" {
		cfg.PrinterName = *printerName
	}
	if *outputDir != "" {
		cfg.OutputDir = *outputDir
	}
	if *noCleanup {
		cfg.CleanupAfter = false
	}

	// Create output directory
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Verify CUPS is available
	if !*dryRun && !cupsPrinterExists(cfg.PrinterName) {
		log.Fatalf("CUPS printer '%s' not found. Available printers:", cfg.PrinterName)
		listCupsPrinters()
		os.Exit(1)
	}

	log.Println("Connecting to Gmail via IMAP...")

	// Connect to Gmail IMAP
	imapClient, err := connectIMAP(cfg.GmailAddress[0], cfg.AppPassword)
	if err != nil {
		log.Fatalf("Failed to connect to Gmail: %v", err)
	}
	defer imapClient.Logout()

	log.Println("Successfully authenticated with Gmail")

	// Fetch attachments from allowed users
	attachments, err := fetchAttachmentsIMAP(imapClient, cfg.AllowedUsers)
	if err != nil {
		log.Fatalf("Failed to fetch attachments: %v", err)
	}

	if len(attachments) == 0 {
		log.Println("No attachments found from allowed users")
		return
	}

	log.Printf("Found %d attachments to process\n", len(attachments))

	// Process each attachment
	successCount := 0
	for _, att := range attachments {
		log.Printf("Processing: %s (From: %s, Email ID: %s)\n", att.Filename, att.From, att.EmailID)

		// Print attachment
		if !*dryRun {
			if err := printAttachmentCUPS(att.LocalPath, cfg.PrinterName); err != nil {
				log.Printf("Error printing %s: %v\n", att.Filename, err)
				continue
			}
			log.Printf("Successfully sent to printer: %s\n", att.Filename)
			successCount++

			// Delete file after successful printing
			if cfg.CleanupAfter {
				if err := os.Remove(att.LocalPath); err != nil {
					log.Printf("Warning: Failed to delete %s: %v\n", att.LocalPath, err)
				} else {
					if *verbose {
						log.Printf("Deleted: %s\n", att.LocalPath)
					}
				}
			}
		} else {
			log.Printf("[DRY-RUN] Would print: %s to printer '%s'\n", att.Filename, cfg.PrinterName)
		}
	}

	if !*dryRun {
		log.Printf("Processing complete - %d/%d files printed successfully\n", successCount, len(attachments))
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
	EmailID   string
	From      string
	Subject   string
	Filename  string
	MimeType  string
	Size      int64
	LocalPath string
}

func fetchAttachmentsIMAP(c *client.Client, allowedUsers []string) ([]Attachment, error) {
	var attachments []Attachment

	// Select INBOX
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

	// Fetch all messages
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(1, mbox.Messages)

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	// Fetch messages with ENVELOPE and RFC822
	go func() {
		done <- c.Fetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchRFC822}, messages)
	}()

	// Process each message
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

		// Check if sender is in allowed list
		if !isAllowedUser(from, allowedUsers) {
			continue
		}

		// Get the message body
		literal := msg.GetBody(&imap.BodySectionName{})
		if literal == nil {
			continue
		}

		// Read from the imap.Literal reader
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(literal); err != nil {
			if *verbose {
				log.Printf("Failed to read message body: %v\n", err)
			}
			continue
		}

		// Parse the message
		mr, err := mail.CreateReader(buf)
		if err != nil {
			if *verbose {
				log.Printf("Failed to parse message: %v\n", err)
			}
			continue
		}

		// Extract attachments from this message
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

	// Iterate through message parts
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}

		// Get Content-Disposition header
		disp := part.Header.Get("Content-Disposition")
		if disp == "" {
			continue
		}

		// Parse the disposition to check if it's an attachment
		mediaType, params, err := mime.ParseMediaType(disp)
		if err != nil {
			continue
		}

		if mediaType != "attachment" {
			continue
		}

		// Get filename from parameters
		filename := params["filename"]
		if filename == "" {
			contentType := part.Header.Get("Content-Type")
			if contentType != "" {
				_, typeParams, _ := mime.ParseMediaType(contentType)
				filename = typeParams["name"]
			}
		}

		if filename == "" {
			filename = "attachment"
		}

		// Read attachment content
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(part.Body); err != nil {
			if *verbose {
				log.Printf("Failed to read attachment: %v\n", err)
			}
			continue
		}

		// Save attachment
		localPath := filepath.Join(*outputDir, filename)
		if err := os.WriteFile(localPath, buf.Bytes(), 0644); err != nil {
			if *verbose {
				log.Printf("Failed to save attachment: %v\n", err)
			}
			continue
		}

		att := Attachment{
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

// printAttachmentCUPS sends file to CUPS printer with format handling
func printAttachmentCUPS(filePath, printerName string) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	// Handle DOCX files - convert to PDF first
	if ext == ".docx" {
		return printDocxViaCUPS(filePath, printerName)
	}

	// Handle PDF files directly
	if ext == ".pdf" {
		return printFileCUPS(filePath, printerName)
	}

	// For other formats, try to print directly
	// CUPS may handle format conversion automatically
	return printFileCUPS(filePath, printerName)
}

// printDocxViaCUPS converts DOCX to PDF and prints
func printDocxViaCUPS(filePath, printerName string) error {
	if *verbose {
		log.Printf("Converting DOCX to PDF: %s\n", filePath)
	}

	// Check if libreoffice is available for conversion
	pdfPath := strings.TrimSuffix(filePath, filepath.Ext(filePath)) + ".pdf"

	// Try to convert DOCX to PDF using libreoffice
	cmd := exec.Command("libreoffice",
		"--headless",
		"--convert-to", "pdf",
		"--outdir", filepath.Dir(filePath),
		filePath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		// Fallback: try to print DOCX directly
		if *verbose {
			log.Printf("LibreOffice conversion failed: %s. Attempting direct print.\n", string(output))
		}
		return printFileCUPS(filePath, printerName)
	}

	if *verbose {
		log.Printf("DOCX converted to: %s\n", pdfPath)
	}

	// Print the converted PDF
	if err := printFileCUPS(pdfPath, printerName); err != nil {
		// Clean up converted PDF on error
		os.Remove(pdfPath)
		return err
	}

	// Clean up the temporary PDF file (keep original DOCX for cleanup later)
	if *verbose {
		log.Printf("Removing temporary PDF: %s\n", pdfPath)
	}
	os.Remove(pdfPath)

	return nil
}

// printFileCUPS uses CUPS lp command to print a file
func printFileCUPS(filePath, printerName string) error {
	// Use CUPS 'lp' command for printing
	// This is lightweight and optimized for embedded systems like Raspberry Pi
	cmd := exec.Command("lp",
		"-d", printerName, // Destination printer
		"-n", "1", // Number of copies
		"-q", "50", // Priority (0-100, 50=normal)
		filePath,
	)

	// Capture output for debugging
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Parse CUPS error messages
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

// cupsPrinterExists checks if a CUPS printer is available
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

// listCupsPrinters lists all available CUPS printers
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
		cfg.PrinterName = "lp" // Default to system default printer
	}

	if cfg.CleanupAfter == false {
		cfg.CleanupAfter = true // Default to cleanup
	}

	return &cfg, nil
}
