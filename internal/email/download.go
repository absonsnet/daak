// Package email provides email content processing utilities
package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/absonsnet/daak/internal/message"
	gomessage "github.com/emersion/go-message"
	msgcharset "github.com/emersion/go-message/charset"
	"golang.org/x/text/encoding/htmlindex"
)

// AttachmentDownloader handles downloading and saving attachments
type AttachmentDownloader struct {
	attachmentsDir string
}

// NewAttachmentDownloader creates a new attachment downloader
func NewAttachmentDownloader(attachmentsDir string) *AttachmentDownloader {
	return &AttachmentDownloader{
		attachmentsDir: attachmentsDir,
	}
}

// ExtractAttachmentContent extracts the content of a specific attachment from raw email bytes
func (d *AttachmentDownloader) ExtractAttachmentContent(raw []byte, targetFilename string) ([]byte, error) {
	return d.ExtractAttachmentContentFor(raw, &message.Attachment{Filename: targetFilename})
}

// ExtractAttachmentContentFor extracts the content of a specific attachment
// record from raw email bytes. When several parts share the same filename,
// the record's ContentID (when set) and Size (when > 0) disambiguate: a
// part whose filename matches but whose size/content-ID does not is kept
// only as a fallback, so the wrong same-named file is not downloaded.
func (d *AttachmentDownloader) ExtractAttachmentContentFor(raw []byte, att *message.Attachment) ([]byte, error) {
	if att == nil {
		return nil, fmt.Errorf("attachment is required")
	}
	targetFilename := att.Filename
	reader := bytes.NewReader(raw)

	entity, err := gomessage.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}

	// We need to find the attachment by matching properties
	if mr := entity.MultipartReader(); mr != nil {
		return d.findAttachmentInMultipart(mr, targetFilename, att)
	}

	// Single-part message: the whole entity may itself be the attachment.
	// entity.Body is already transfer-decoded by go-message — decoding
	// again here would corrupt content that happens to look base64-encoded.
	if getFilename(entity) == targetFilename {
		content, err := io.ReadAll(entity.Body)
		if err != nil {
			return nil, err
		}
		return content, nil
	}

	return nil, fmt.Errorf("attachment not found: %s", targetFilename)
}

// attachmentSearch carries the fallback for a same-filename search: the
// first filename match whose size/content-ID did not fit, used only when
// no exact match exists anywhere in the message.
type attachmentSearch struct {
	match       *message.Attachment
	found       []byte
	hasFound    bool
	fallback    []byte
	hasFallback bool
}

// matches reports whether content read from a part with the given
// Content-ID is the attachment being searched for. A mismatch is not a
// hard rejection — the content is kept as fallback.
func (s *attachmentSearch) matches(content []byte, contentID string) bool {
	if s.match == nil {
		return true
	}
	if s.match.ContentID != "" && contentID != s.match.ContentID {
		return false
	}
	if s.match.Size > 0 && len(content) != s.match.Size {
		return false
	}
	return true
}

func (s *attachmentSearch) consider(content []byte, contentID string) ([]byte, bool) {
	if s.matches(content, contentID) {
		s.found = content
		s.hasFound = true
		return content, true
	}
	if !s.hasFallback {
		s.fallback = content
		s.hasFallback = true
	}
	return nil, false
}

// InlineAttachmentResult holds content-id to data URL mapping
type InlineAttachmentResult struct {
	ContentID   string
	ContentType string
	Content     []byte
}

// ExtractInlineAttachments extracts all inline attachments from raw email bytes
// Returns a map of content-id to base64 data URL
func (d *AttachmentDownloader) ExtractInlineAttachments(raw []byte) (map[string]string, error) {
	reader := bytes.NewReader(raw)

	entity, err := gomessage.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}

	result := make(map[string]string)

	if mr := entity.MultipartReader(); mr != nil {
		d.findInlineAttachmentsInMultipart(mr, result)
	}

	return result, nil
}

// findInlineAttachmentsInMultipart searches for inline attachments and builds data URLs
func (d *AttachmentDownloader) findInlineAttachmentsInMultipart(mr gomessage.MultipartReader, result map[string]string) {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		// Handle nested multipart
		if nestedMr := part.MultipartReader(); nestedMr != nil {
			d.findInlineAttachmentsInMultipart(nestedMr, result)
			continue
		}

		// Check for Content-ID header (indicates inline attachment)
		contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")
		if contentID == "" {
			continue
		}

		// Get content type
		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		// Read content (already transfer-decoded by go-message)
		content, err := io.ReadAll(part.Body)
		if err != nil {
			continue
		}

		// Build data URL
		dataURL := buildDataURL(contentType, content)
		result[contentID] = dataURL
	}
}

// buildDataURL creates a data URL from content type and binary content
func buildDataURL(contentType string, content []byte) string {
	encoded := base64.StdEncoding.EncodeToString(content)
	return fmt.Sprintf("data:%s;base64,%s", contentType, encoded)
}

// findAttachmentInMultipart searches for an attachment by filename in a multipart message.
// When match is non-nil, parts sharing the filename are disambiguated by
// Content-ID and Size; a filename-only match is kept as fallback.
func (d *AttachmentDownloader) findAttachmentInMultipart(mr gomessage.MultipartReader, targetFilename string, match *message.Attachment) ([]byte, error) {
	search := &attachmentSearch{match: match}
	if err := d.findAttachmentInMultipartSearch(mr, targetFilename, search); err != nil {
		return nil, err
	}
	if search.hasFound {
		return search.found, nil
	}
	if search.hasFallback {
		return search.fallback, nil
	}
	return nil, fmt.Errorf("attachment not found: %s", targetFilename)
}

func (d *AttachmentDownloader) findAttachmentInMultipartSearch(mr gomessage.MultipartReader, targetFilename string, search *attachmentSearch) error {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			continue
		}

		// Handle nested multipart
		if nestedMr := part.MultipartReader(); nestedMr != nil {
			if err := d.findAttachmentInMultipartSearch(nestedMr, targetFilename, search); err != nil {
				return err
			}
			if search.hasFound {
				return nil
			}
			continue
		}

		// TNEF (winmail.dat): the requested file may live INSIDE the container.
		// The sync extractor stored the inner TNEF Title as the filename, so match
		// the target against the decoded inner attachments. part.Body is already
		// transfer-decoded by go-message, so it feeds tnef.Decode directly.
		contentType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if contentType == "application/ms-tnef" ||
			(disposition == "attachment" && strings.EqualFold(dispParams["filename"], "winmail.dat")) {
			raw, err := io.ReadAll(part.Body)
			if err != nil {
				continue
			}
			for _, inner := range DecodeTNEFAttachments(raw) {
				if inner.Filename == targetFilename {
					if _, ok := search.consider(inner.Content, ""); ok {
						return nil
					}
				}
			}
			// Decode failed or no inner match: only the container itself can satisfy
			// a request for "winmail.dat"; otherwise move on.
			if strings.EqualFold(targetFilename, "winmail.dat") {
				if _, ok := search.consider(raw, ""); ok {
					return nil
				}
			}
			continue
		}

		// Check filename
		filename := getFilename(part)
		if filename == targetFilename {
			// part.Body is already transfer-decoded by go-message — do not
			// decode again: decoded content that happens to be valid base64
			// would silently come back as shorter garbage bytes.
			content, err := io.ReadAll(part.Body)
			if err != nil {
				return err
			}
			contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")
			if _, ok := search.consider(content, contentID); ok {
				return nil
			}
		}
	}
}

// messageSubdir returns the attachments subdirectory for a message ID:
// the first 8 characters when present, the whole ID when shorter, and
// "unknown" for an empty ID (a temporary record with no ID must never
// slice out of range).
func messageSubdir(messageID string) string {
	if messageID == "" {
		return "unknown"
	}
	if len(messageID) >= 8 {
		return messageID[:8]
	}
	return messageID
}

// DeduplicateFilename returns a filename that does not collide with the
// names already used in this batch (used) nor with a file already on
// disk in dir ("" skips the disk check). "report.pdf" becomes
// "report (2).pdf", then "report (3).pdf", and so on.
func DeduplicateFilename(dir, filename string, used map[string]bool) string {
	if used == nil {
		used = map[string]bool{}
	}
	candidate := filename
	ext := filepath.Ext(filename)
	base := filename[:len(filename)-len(ext)]
	for i := 2; ; i++ {
		if used[candidate] {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
			continue
		}
		if dir != "" {
			if _, err := os.Stat(filepath.Join(dir, candidate)); err == nil {
				candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
				continue
			}
		}
		break
	}
	used[candidate] = true
	return candidate
}

// decodeMIMEFilename decodes a MIME-encoded filename with full charset support.
// Mirrors the sync code's decodeMIMEWord() to ensure filenames match between
// sync (when stored to DB) and download (when extracting from raw message).
func decodeMIMEFilename(s string) string {
	if s == "" {
		return s
	}
	dec := &mime.WordDecoder{
		CharsetReader: func(charsetName string, r io.Reader) (io.Reader, error) {
			if reader, err := msgcharset.Reader(charsetName, r); err == nil {
				return reader, nil
			}
			enc, err := htmlindex.Get(charsetName)
			if err != nil {
				return nil, fmt.Errorf("unknown charset: %s", charsetName)
			}
			return enc.NewDecoder().Reader(r), nil
		},
	}
	decoded, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

// getFilename extracts the filename from a message part
func getFilename(part *gomessage.Entity) string {
	// Try Content-Disposition first
	if disp := part.Header.Get("Content-Disposition"); disp != "" {
		_, params, _ := mime.ParseMediaType(disp)
		if filename := params["filename"]; filename != "" {
			return decodeMIMEFilename(filename)
		}
	}

	// Try Content-Type name parameter
	if ct := part.Header.Get("Content-Type"); ct != "" {
		_, params, _ := mime.ParseMediaType(ct)
		if name := params["name"]; name != "" {
			return decodeMIMEFilename(name)
		}
	}

	// Synthetic fallback: match sync/parse.go extractAttachmentMetadata logic
	contentType := "application/octet-stream"
	if ct := part.Header.Get("Content-Type"); ct != "" {
		mt, _, _ := mime.ParseMediaType(ct)
		if mt != "" {
			contentType = mt
		}
	}

	return FallbackFilename(contentType)
}

// SaveAttachment saves attachment content to disk
func (d *AttachmentDownloader) SaveAttachment(att *message.Attachment, content []byte, customPath string) (string, error) {
	var savePath string

	if customPath != "" {
		// Use custom path provided by user
		savePath = customPath
	} else {
		// Save to default attachments directory
		// Create subdirectory based on message ID for organization
		subDir := filepath.Join(d.attachmentsDir, messageSubdir(att.MessageID))
		if err := os.MkdirAll(subDir, 0700); err != nil {
			return "", fmt.Errorf("failed to create attachment directory: %w", err)
		}

		// Generate unique filename to avoid conflicts
		safeName := filepath.Base(att.Filename)
		savePath = filepath.Join(subDir, safeName)

		// If file exists, append a number
		if _, err := os.Stat(savePath); err == nil {
			ext := filepath.Ext(safeName)
			base := safeName[:len(safeName)-len(ext)]
			for i := 1; ; i++ {
				savePath = filepath.Join(subDir, fmt.Sprintf("%s_%d%s", base, i, ext))
				if _, err := os.Stat(savePath); os.IsNotExist(err) {
					break
				}
			}
		}
	}

	// Write content to file
	if err := os.WriteFile(savePath, content, 0600); err != nil {
		return "", fmt.Errorf("failed to write attachment: %w", err)
	}

	return savePath, nil
}
