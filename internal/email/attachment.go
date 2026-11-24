// Package email provides email content processing utilities
package email

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strings"

	"github.com/absonsnet/daak/internal/message"
	gomessage "github.com/emersion/go-message"
	"github.com/google/uuid"
	"github.com/teamwork/tnef"
)

// AttachmentExtractor extracts attachment metadata and content from emails
type AttachmentExtractor struct{}

// NewAttachmentExtractor creates a new attachment extractor
func NewAttachmentExtractor() *AttachmentExtractor {
	return &AttachmentExtractor{}
}

// FallbackFilename generates a filename for a MIME part that declared none,
// derived from its content type. Every site that names or re-locates
// nameless parts (sync-time extraction in sync/parse.go, this extractor,
// and the downloader's part matching in download.go) MUST use this one
// function — the downloader finds parts by comparing the STORED filename
// against the same synthesis, so divergent fallbacks break Save/Open of
// nameless attachments (#370).
func FallbackFilename(contentType string) string {
	// Outlook/Teams meeting invites: a recognizable, openable name
	// instead of attachment.bin (#370)
	if contentType == "text/calendar" {
		return "invite.ics"
	}
	// Keep the historical image/<subtype> naming exactly (ExtensionsByType
	// could pick a different-but-valid extension per platform)
	if strings.HasPrefix(contentType, "image/") {
		parts := strings.SplitN(contentType, "/", 2)
		if len(parts) == 2 && parts[1] != "" {
			return "attachment." + parts[1]
		}
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return "attachment" + exts[0]
	}
	return "attachment.bin"
}

// AttachmentData holds both metadata and content for an attachment
type AttachmentData struct {
	Attachment *message.Attachment
	Content    []byte
}

// ExtractAttachments extracts all attachments from a raw email message
func (e *AttachmentExtractor) ExtractAttachments(messageID string, raw []byte) ([]*AttachmentData, error) {
	reader := bytes.NewReader(raw)

	entity, err := gomessage.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}

	// Check if it's a multipart message
	if mr := entity.MultipartReader(); mr != nil {
		return e.extractFromMultipart(messageID, mr), nil
	}

	// Single-part message: the whole entity may itself be the attachment
	// (e.g. a DMARC report whose body is application/zip). Without this
	// branch a single-part attachment lists no downloadable rows.
	return e.extractFromSinglePart(messageID, entity), nil
}

// extractFromSinglePart classifies a single-part entity as an attachment,
// mirroring the sync parser's parseSinglePartBody classification so the two
// paths agree on what counts: an explicit attachment disposition, or a
// non-text body that is not a signature part.
func (e *AttachmentExtractor) extractFromSinglePart(messageID string, entity *gomessage.Entity) []*AttachmentData {
	contentType, params, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
	disposition, dispParams, _ := mime.ParseMediaType(entity.Header.Get("Content-Disposition"))
	contentID := strings.Trim(entity.Header.Get("Content-ID"), "<>")

	// Signature parts are never downloadable attachments.
	if isSignaturePart(contentType) {
		return nil
	}

	isNonTextBody := contentType != "" && !strings.HasPrefix(contentType, "text/") &&
		!isSignaturePart(contentType)
	if disposition != "attachment" && !isNonTextBody {
		return nil
	}

	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	if filename == "" {
		filename = FallbackFilename(contentType)
	}
	if decodedFilename, err := decodeRFC2047(filename); err == nil {
		filename = decodedFilename
	}

	// entity.Body is already transfer-decoded by go-message.
	content, err := io.ReadAll(entity.Body)
	if err != nil {
		return nil
	}

	isInline := disposition == "inline" || contentID != ""
	att := &message.Attachment{
		ID:          uuid.New().String(),
		MessageID:   messageID,
		Filename:    filename,
		ContentType: contentType,
		Size:        len(content),
		ContentID:   contentID,
		IsInline:    isInline && contentID != "",
	}

	return []*AttachmentData{{Attachment: att, Content: content}}
}

// isSignaturePart returns true for S/MIME and PGP signature content types
// that should not be exposed as downloadable attachments. Must mirror
// isSignaturePart in internal/sync/parse.go — the two cannot share one
// (sync imports this package, so the reverse import would be a cycle).
// The sync parser never sees these parts (signed messages are unwrapped
// before its walk) but this extractor walks raw bytes, so the exclusion
// has to live here too.
func isSignaturePart(contentType string) bool {
	switch contentType {
	case "application/pkcs7-signature", "application/x-pkcs7-signature",
		"application/pgp-signature":
		return true
	}
	return false
}

// extractFromMultipart extracts attachments from a multipart message
func (e *AttachmentExtractor) extractFromMultipart(messageID string, mr gomessage.MultipartReader) []*AttachmentData {
	var attachments []*AttachmentData

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		contentType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")

		// Handle nested multipart
		if strings.HasPrefix(contentType, "multipart/") {
			if nestedMr := part.MultipartReader(); nestedMr != nil {
				nested := e.extractFromMultipart(messageID, nestedMr)
				attachments = append(attachments, nested...)
			}
			continue
		}

		// Check for TNEF (winmail.dat)
		if contentType == "application/ms-tnef" ||
			(disposition == "attachment" && strings.EqualFold(dispParams["filename"], "winmail.dat")) {
			tnefAttachments := e.extractFromTNEF(messageID, part.Body)
			attachments = append(attachments, tnefAttachments...)
			continue
		}

		// Signature parts (S/MIME, PGP) are not downloadable attachments:
		// without this a signed message gains spurious signature files.
		if isSignaturePart(contentType) {
			continue
		}

		// Determine if this is an attachment
		isAttachment := disposition == "attachment"
		isInline := disposition == "inline" || contentID != ""

		// Skip text/plain and text/html unless they're explicit attachments
		if !isAttachment && (contentType == "text/plain" || contentType == "text/html") {
			continue
		}

		// If it's not text and has content, treat it as an attachment
		if isAttachment || isInline || (!strings.HasPrefix(contentType, "text/") && contentType != "") {
			// Get filename
			filename := dispParams["filename"]
			if filename == "" {
				filename = params["name"]
			}
			if filename == "" {
				filename = FallbackFilename(contentType)
			}

			// Decode filename if encoded
			decodedFilename, err := decodeRFC2047(filename)
			if err == nil {
				filename = decodedFilename
			}

			// Read content (already transfer-decoded by go-message — do not
			// decode again: decoded content that happens to be valid base64
			// would silently come back as shorter garbage bytes).
			content, err := io.ReadAll(part.Body)
			if err != nil {
				continue
			}

			att := &message.Attachment{
				ID:          uuid.New().String(),
				MessageID:   messageID,
				Filename:    filename,
				ContentType: contentType,
				Size:        len(content),
				ContentID:   contentID,
				IsInline:    isInline && contentID != "",
			}

			attachments = append(attachments, &AttachmentData{
				Attachment: att,
				Content:    content,
			})
		}
	}

	return attachments
}

// TNEFAttachment is a single attachment decoded from a TNEF (winmail.dat) container.
type TNEFAttachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// DecodeTNEFAttachments decodes a TNEF (winmail.dat) container into its inner
// attachments, returning nil if the bytes are not valid TNEF. This is the single
// source of TNEF decoding shared by the sync extractor, the on-demand extractor,
// and the downloader, so the three paths can never diverge on filename/type — the
// invariant that lets sync store a name the downloader can later resolve.
func DecodeTNEFAttachments(data []byte) []TNEFAttachment {
	tnefData, err := tnef.Decode(data)
	if err != nil {
		return nil
	}

	var out []TNEFAttachment
	for _, att := range tnefData.Attachments {
		filename := att.Title
		if filename == "" {
			filename = "attachment"
		}

		// Guess content type from the filename extension.
		contentType := "application/octet-stream"
		if guessed := mime.TypeByExtension(filepath.Ext(filename)); guessed != "" {
			contentType = guessed
		}

		out = append(out, TNEFAttachment{
			Filename:    filename,
			ContentType: contentType,
			Content:     att.Data,
		})
	}

	return out
}

// extractFromTNEF extracts attachments from a TNEF (winmail.dat) file
func (e *AttachmentExtractor) extractFromTNEF(messageID string, reader io.Reader) []*AttachmentData {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil
	}

	var attachments []*AttachmentData
	for _, tnefAtt := range DecodeTNEFAttachments(data) {
		attachments = append(attachments, &AttachmentData{
			Attachment: &message.Attachment{
				ID:          uuid.New().String(),
				MessageID:   messageID,
				Filename:    tnefAtt.Filename,
				ContentType: tnefAtt.ContentType,
				Size:        len(tnefAtt.Content),
				IsInline:    false,
			},
			Content: tnefAtt.Content,
		})
	}

	return attachments
}

// decodeRFC2047 decodes RFC 2047 encoded strings (like filenames)
func decodeRFC2047(s string) (string, error) {
	dec := new(mime.WordDecoder)
	return dec.DecodeHeader(s)
}
