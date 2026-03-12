package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Config struct {
	AllowedUsers []string `json:"allowed_users"`
	PrinterHost  string   `json:"printer_host"`
	PrinterPort  int      `json:"printer_port"`
	OutputDir    string   `json:"output_directory"`
}

var (
	credentialsFile = flag.String("credentials", "credentials.json", "Path to service account credentials JSON file")
	configFile      = flag.String("config", "config.json", "Path to configuration file")
	printerName     = flag.String("printer", "", "Network printer name/address (overrides config)")
	outputDir       = flag.String("output", "/tmp/print-attachments", "Output directory for attachments")
	markAsRead      = flag.Bool("mark-read", false, "Mark processed emails as read")
	dryRun          = flag.Bool("dry-run", false, "Show what would be printed without actually printing")
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
		cfg.PrinterHost = *printerName
	}
	if *outputDir != "" {
		cfg.OutputDir = *outputDir
	}

	// Create output directory
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Authenticate with Gmail
	ctx := context.Background()
	gmailService, err := authenticateGmail(ctx, *credentialsFile)
	if err != nil {
		log.Fatalf("Failed to authenticate with Gmail: %v", err)
	}

	log.Println("Successfully authenticated with Gmail")

	// Get emails from allowed users
	attachments, err := fetchAttachments(gmailService, cfg.AllowedUsers)
	if err != nil {
		log.Fatalf("Failed to fetch attachments: %v", err)
	}

	if len(attachments) == 0 {
		log.Println("No attachments found from allowed users")
		return
	}

	log.Printf("Found %d attachments to process\n", len(attachments))

	// Process each attachment
	for _, att := range attachments {
		log.Printf("Processing: %s (Email ID: %s)\n", att.Filename, att.EmailID)

		// Save attachment
		if err := saveAttachment(gmailService, att); err != nil {
			log.Printf("Error saving attachment %s: %v\n", att.Filename, err)
			continue
		}

		// Print attachment if dry-run is disabled
		if !*dryRun {
			if err := printAttachment(att.LocalPath, cfg.PrinterHost, cfg.PrinterPort); err != nil {
				log.Printf("Error printing %s: %v\n", att.Filename, err)
				continue
			}
			log.Printf("Successfully printed: %s\n", att.Filename)
		} else {
			log.Printf("[DRY-RUN] Would print: %s to %s:%d\n", att.Filename, cfg.PrinterHost, cfg.PrinterPort)
		}

		// Mark email as read if requested
		if *markAsRead && !*dryRun {
			if err := markEmailAsRead(gmailService, att.EmailID); err != nil {
				log.Printf("Warning: Failed to mark email as read: %v\n", err)
			}
		}
	}

	log.Println("Processing complete")
}

func authenticateGmail(ctx context.Context, credentialsPath string) (*gmail.Service, error) {
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	// For service account credentials
	config, err := google.JWTConfigFromJSON(data, gmail.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	// Create HTTP client with JWT
	httpClient := config.Client(ctx)

	// Create Gmail service
	service, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gmail service: %w", err)
	}

	return service, nil
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

func fetchAttachments(srv *gmail.Service, allowedUsers []string) ([]Attachment, error) {
	var attachments []Attachment

	// Build query for allowed users
	query := buildUserQuery(allowedUsers)
	log.Printf("Searching with query: %s\n", query)

	// List messages
	call := srv.Users.Messages.List("me").Q(query).MaxResults(10)

	for {
		resp, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list messages: %w", err)
		}

		if resp.Messages == nil {
			break
		}

		// Get details for each message
		for _, msg := range resp.Messages {
			message, err := srv.Users.Messages.Get("me", msg.Id).Format("full").Do()
			if err != nil {
				log.Printf("Failed to get message %s: %v\n", msg.Id, err)
				continue
			}

			from := getHeaderValue(message.Payload.Headers, "From")
			subject := getHeaderValue(message.Payload.Headers, "Subject")

			// Extract attachments
			att := extractMessageAttachments(srv, message, from, subject)
			attachments = append(attachments, att...)
		}

		// Check if there are more pages
		if resp.NextPageToken == "" {
			break
		}
		call.PageToken(resp.NextPageToken)
	}

	return attachments, nil
}

func extractMessageAttachments(srv *gmail.Service, message *gmail.Message, from, subject string) []Attachment {
	var attachments []Attachment

	if message.Payload == nil {
		return attachments
	}

	// Check for attachments in main payload
	for _, part := range message.Payload.Parts {
		if part.Filename != "" && part.Body.AttachmentId != "" {
			att := Attachment{
				EmailID:  message.Id,
				From:     from,
				Subject:  subject,
				Filename: part.Filename,
				MimeType: part.MimeType,
				Size:     part.Body.Size,
			}
			attachments = append(attachments, att)
		}

		// Recursively check nested parts
		nested := extractMessageAttachments(srv, &gmail.Message{Payload: part}, from, subject)
		attachments = append(attachments, nested...)
	}

	return attachments
}

func saveAttachment(srv *gmail.Service, att Attachment) error {
	// Get attachment data
	resp, err := srv.Users.Messages.Attachments.Get("me", att.EmailID, att.Filename).Do()
	if err != nil {
		return fmt.Errorf("failed to get attachment: %w", err)
	}

	// Decode the data
	data := resp.Data
	if data == "" {
		return fmt.Errorf("empty attachment data")
	}

	// Save to file
	localPath := filepath.Join(*outputDir, att.Filename)
	if err := os.WriteFile(localPath, []byte(data), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	att.LocalPath = localPath
	return nil
}

func printAttachment(filePath, printerHost string, printerPort int) error {
	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Connect to printer (raw socket connection for AppSocket/JetDirect)
	// Use net.JoinHostPort to properly handle IPv6 addresses
	addr := net.JoinHostPort(printerHost, strconv.Itoa(printerPort))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to printer: %w", err)
	}
	defer conn.Close()

	// Send file to printer
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("failed to send data to printer: %w", err)
	}

	return nil
}

func markEmailAsRead(srv *gmail.Service, messageID string) error {
	// Remove UNREAD label
	_, err := srv.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{"UNREAD"},
	}).Do()
	return err
}

func buildUserQuery(allowedUsers []string) string {
	if len(allowedUsers) == 0 {
		return "has:attachment"
	}

	var conditions []string
	for _, user := range allowedUsers {
		conditions = append(conditions, fmt.Sprintf("from:%s", user))
	}

	query := strings.Join(conditions, " OR ")
	return fmt.Sprintf("(%s) has:attachment", query)
}

func getHeaderValue(headers []*gmail.MessagePartHeader, name string) string {
	for _, header := range headers {
		if header.Name == name {
			return header.Value
		}
	}
	return ""
}

func loadConfig(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		// Return default config if file doesn't exist
		if os.IsNotExist(err) {
			return &Config{
				PrinterPort: 9100,
				OutputDir:   *outputDir,
			}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.PrinterPort == 0 {
		cfg.PrinterPort = 9100 // Default AppSocket port
	}

	return &cfg, nil
}
