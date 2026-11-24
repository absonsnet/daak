package sync

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/email"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/smime"
	gomessage "github.com/emersion/go-message"
)

// maxConsecutiveNextPartErrors bounds the skip-and-continue recovery in
// parseMultipartBody so a permanently-erroring reader can't spin forever.
const maxConsecutiveNextPartErrors = 20

// parseMessageBodyFull parses a raw email and extracts text, HTML, and attachment metadata.
// Attachments are extracted during the same parsing pass - no re-parsing needed.
// For inline images, content is also captured (up to maxInlineContentSize) for display.
// For file attachments, only metadata is captured - content fetched on-demand.
// messageID is needed to create attachment records.
func (e *Engine) parseMessageBodyFull(raw []byte, messageID string, timeout time.Duration) *ParsedBody {
	type result struct {
		parsed *ParsedBody
	}

	// Use buffered channel to prevent goroutine leak if timeout fires
	done := make(chan result, 1)

	go func() {
		parsed := e.parseMessageBodyInternal(raw, messageID)
		select {
		case done <- result{parsed}:
		default:
		}
	}()

	select {
	case r := <-done:
		return r.parsed
	case <-time.After(timeout):
		e.log.Warn().
			Int("rawLen", len(raw)).
			Dur("timeout", timeout).
			Msg("Body parsing timed out - attempting fallback extraction")

		partialText := e.extractPlainTextFallback(raw)
		return &ParsedBody{
			BodyText:       partialText,
			BodyHTML:       "",
			HasAttachments: false,
			Attachments:    nil,
		}
	}
}

// parseMessageBodyInternal does the actual parsing work, extracting body text, HTML, and attachments.
func (e *Engine) parseMessageBodyInternal(raw []byte, messageID string) *ParsedBody {
	result := &ParsedBody{}
	reader := bytes.NewReader(raw)

	entity, err := gomessage.Read(reader)
	if err != nil {
		e.log.Debug().Err(err).Int("rawLen", len(raw)).Msg("Failed to parse message, trying as plain text")
		result.BodyText = string(raw)
		return result
	}

	topLevelCT := entity.Header.Get("Content-Type")
	e.log.Debug().
		Str("topLevelContentType", topLevelCT).
		Int("rawLen", len(raw)).
		Msg("Parsing message body")

	// Check for S/MIME content (signed or encrypted)
	isSigned := smime.IsSMIMESigned(topLevelCT)
	isEncrypted := smime.IsSMIMEEncrypted(topLevelCT)

	if isSigned || isEncrypted {
		// Store raw body for on-view processing (verification/decryption happens fresh on each view)
		result.SMIMERawBody = raw
		result.SMIMEEncrypted = isEncrypted

		if isEncrypted {
			// Encrypted: don't store body text/html (decrypted only on view)
			return result
		}

		// Signed-only: still parse body for FTS, but don't cache verification status
		if e.smimeVerifier != nil {
			_, innerBody := e.smimeVerifier.VerifyAndUnwrap(raw)
			// Use the unwrapped inner body for parsing (not the S/MIME wrapper)
			if innerBody != nil {
				raw = innerBody
				reader = bytes.NewReader(raw)
				newEntity, parseErr := gomessage.Read(reader)
				if parseErr != nil {
					e.log.Debug().Err(parseErr).Msg("Failed to re-parse unwrapped S/MIME body")
					result.BodyText = string(raw)
					return result
				}
				entity = newEntity
				topLevelCT = entity.Header.Get("Content-Type")
			}
		}
	}

	// Check for PGP/MIME content (signed or encrypted)
	isPGPSigned := pgp.IsPGPSigned(topLevelCT)
	isPGPEncrypted := pgp.IsPGPEncrypted(topLevelCT)

	if isPGPSigned || isPGPEncrypted {
		// Store raw body for on-view processing (verification/decryption happens fresh on each view)
		result.PGPRawBody = raw
		result.PGPEncrypted = isPGPEncrypted

		if isPGPEncrypted {
			// Encrypted: don't store body text/html (decrypted only on view)
			return result
		}

		// Signed-only: still parse body for FTS, but don't cache verification status
		if e.pgpVerifier != nil {
			_, innerBody := e.pgpVerifier.VerifyAndUnwrap(raw)
			// Use the unwrapped inner body for parsing (not the PGP wrapper)
			if innerBody != nil {
				raw = innerBody
				reader = bytes.NewReader(raw)
				newEntity, parseErr := gomessage.Read(reader)
				if parseErr != nil {
					e.log.Debug().Err(parseErr).Msg("Failed to re-parse unwrapped PGP body")
					result.BodyText = string(raw)
					return result
				}
				entity = newEntity
			}
		}
	}

	mr := entity.MultipartReader()
	e.log.Debug().Bool("isMultipart", mr != nil).Msg("Multipart detection result")

	if mr != nil {
		e.parseMultipartBody(mr, result, messageID)
	}
	if mr == nil {
		e.parseSinglePartBody(entity, result, messageID)
	}

	// Email reactions: a Gmail reaction part or an RFC 9078 reaction part
	// makes the whole message a reaction, not a regular email. Detection
	// runs after the normal walk so any fallback text in the message is
	// kept as the body (used when the reaction can't be linked to its
	// original and falls back to a regular email); when no fallback text
	// exists the emoji itself becomes the body text.
	if reaction := detectReaction(raw); reaction != nil {
		result.Reaction = reaction
		if result.BodyText == "" {
			result.BodyText = reaction.Emoji
		}
	} else if fb := detectOutlookFallbackReaction(outlookFallbackText(result)); fb != nil {
		// Outlook's fallback reaction email carries no machine-readable
		// reaction part — only the banner line plus the quoted original —
		// so it is recognised from the body text instead. Linking and
		// hiding then work exactly as for the MIME formats.
		result.Reaction = fb
	}

	e.log.Debug().
		Int("bodyTextLen", len(result.BodyText)).
		Int("bodyHTMLLen", len(result.BodyHTML)).
		Bool("hasAttachments", result.HasAttachments).
		Int("attachmentCount", len(result.Attachments)).
		Msg("parseMessageBody complete")

	return result
}

// parseMultipartBody parses a multipart message body
func (e *Engine) parseMultipartBody(mr gomessage.MultipartReader, result *ParsedBody, messageID string) {
	partIndex := 0
	consecutiveErrors := 0
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") {
				e.log.Debug().Int("partsProcessed", partIndex).Msg("Finished reading multipart parts")
				break
			}
			// go-message returns both the part AND an error for unknown CTE.
			// Mark as unsafe and stop — don't render content with invalid encoding.
			if gomessage.IsUnknownEncoding(err) {
				e.log.Warn().Err(err).Int("partsProcessed", partIndex).Int("attachmentsCollected", len(result.Attachments)).Msg("Unknown encoding in multipart part, marking unsafe")
				result.UnsafeContent = true
				result.BodyText = "This message uses non-standard encoding and cannot be displayed safely."
				return
			}
			// A single malformed part must not sacrifice the siblings after it: skip
			// it and keep walking (mirrors email/attachment.go). Bounded so a reader
			// that errors forever can't spin.
			consecutiveErrors++
			e.log.Debug().Err(err).Int("partIndex", partIndex).Int("consecutiveErrors", consecutiveErrors).Msg("Error reading multipart part, skipping")
			if consecutiveErrors > maxConsecutiveNextPartErrors {
				e.log.Warn().Err(err).Int("partsProcessed", partIndex).Msg("Too many consecutive multipart errors, stopping walk")
				break
			}
			continue
		}
		consecutiveErrors = 0
		partIndex++

		contentType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		disposition, dispParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		contentID := strings.Trim(part.Header.Get("Content-ID"), "<>")

		e.log.Debug().
			Int("partIndex", partIndex).
			Str("contentType", contentType).
			Str("disposition", disposition).
			Str("contentID", contentID).
			Str("charset", params["charset"]).
			Msg("Processing multipart part")

		// TNEF (winmail.dat): Outlook wraps the real attachments inside an
		// application/ms-tnef container. Decode it and surface the inner files so
		// they aren't lost; the downloader resolves them by the same Title. Must run
		// before the disposition=="attachment" branch (winmail.dat has that
		// disposition). part.Body is already transfer-decoded by go-message.
		if contentType == "application/ms-tnef" ||
			(disposition == "attachment" && strings.EqualFold(dispParams["filename"], "winmail.dat")) {
			// Read maxPartSize+1 so a part landing exactly on the cap is
			// not misread as truncated (finding 26).
			raw, _ := io.ReadAll(io.LimitReader(part.Body, maxPartSize+1))
			if int64(len(raw)) > maxPartSize {
				result.Truncated = true
				raw = raw[:maxPartSize]
			}
			if inner := email.DecodeTNEFAttachments(raw); len(inner) > 0 {
				for _, ta := range inner {
					result.Attachments = append(result.Attachments, &message.Attachment{
						ID:          generateID(),
						MessageID:   messageID,
						Filename:    ta.Filename,
						ContentType: ta.ContentType,
						Size:        len(ta.Content),
						IsInline:    false,
					})
				}
				result.HasAttachments = true
				e.log.Debug().Int("partIndex", partIndex).Int("tnefAttachments", len(inner)).Msg("Classified part: tnef (decoded inner attachments)")
				continue
			}
			// Decode failed/empty: surface winmail.dat itself so the user sees an
			// attachment rather than nothing.
			fallbackName := dispParams["filename"]
			if fallbackName == "" {
				fallbackName = params["name"]
			}
			if fallbackName == "" {
				fallbackName = "winmail.dat"
			}
			result.Attachments = append(result.Attachments, &message.Attachment{
				ID:          generateID(),
				MessageID:   messageID,
				Filename:    fallbackName,
				ContentType: "application/ms-tnef",
				Size:        len(raw),
				IsInline:    false,
			})
			result.HasAttachments = true
			e.log.Debug().Int("partIndex", partIndex).Msg("Classified part: tnef (decode failed/empty, surfaced winmail.dat)")
			continue
		}

		// Handle file attachments
		if disposition == "attachment" {
			result.HasAttachments = true
			// If the attachment has a Content-ID, it's meant to be displayed inline in the HTML
			// (referenced via cid:contentID), even if Content-Disposition says "attachment"
			isInline := contentID != ""
			att := e.extractAttachmentMetadata(part, messageID, contentType, dispParams, contentID, isInline)
			if att != nil {
				result.Attachments = append(result.Attachments, att)
			}
			e.log.Debug().Int("partIndex", partIndex).Str("branch", "attachment").Msg("Classified part")
			continue
		}

		// Handle nested multipart
		if strings.HasPrefix(contentType, "multipart/") {
			nestedMr := part.MultipartReader()
			if nestedMr == nil {
				e.log.Debug().Int("partIndex", partIndex).Str("contentType", contentType).Msg("Nested multipart has nil reader, subtree skipped")
				continue
			}
			e.parseMultipartBody(nestedMr, result, messageID)
			continue
		}

		// Handle inline images (explicit inline disposition OR image with Content-ID)
		// Many emails have images with Content-ID but no Content-Disposition header
		if (disposition == "inline" && strings.HasPrefix(contentType, "image/")) ||
			(contentID != "" && strings.HasPrefix(contentType, "image/")) {
			result.HasAttachments = true
			att := e.extractAttachmentMetadata(part, messageID, contentType, dispParams, contentID, true)
			if att != nil {
				result.Attachments = append(result.Attachments, att)
			}
			e.log.Debug().Int("partIndex", partIndex).Str("branch", "inline-image").Msg("Classified part")
			continue
		}

		// Handle non-text, non-image parts with inline disposition (e.g. inline PDF)
		// or no disposition at all — these are implicit attachments
		if contentType != "" && !strings.HasPrefix(contentType, "text/") &&
			!isSignaturePart(contentType) {
			result.HasAttachments = true
			isInline := disposition == "inline" || contentID != ""
			att := e.extractAttachmentMetadata(part, messageID, contentType, dispParams, contentID, isInline)
			if att != nil {
				result.Attachments = append(result.Attachments, att)
			}
			e.log.Debug().Int("partIndex", partIndex).Str("branch", "implicit-attachment").Msg("Classified part")
			continue
		}

		// Check for invalid Content-Transfer-Encoding before reading.
		// go-message applies transfer decoding via part.Body, which fails on unknown encodings.
		// Invalid CTE is a common spammer technique — refuse to render for safety.
		cte := strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")))
		if cte != "" && cte != "7bit" && cte != "8bit" && cte != "binary" &&
			cte != "quoted-printable" && cte != "base64" {
			e.log.Warn().Str("cte", cte).Int("partIndex", partIndex).Int("attachmentsCollected", len(result.Attachments)).Msg("Non-standard Content-Transfer-Encoding, marking unsafe")
			result.UnsafeContent = true
			result.BodyText = "This message uses non-standard encoding and cannot be displayed safely."
			result.BodyHTML = ""
			return
		}

		// Read text parts. The limit is maxPartSize+1 so a part landing
		// exactly on the cap is not misread as truncated: only a read
		// past the cap means bytes were cut (finding 26).
		lr := io.LimitReader(part.Body, maxPartSize+1)
		partBody, err := io.ReadAll(lr)
		if err != nil {
			if len(partBody) > 0 {
				e.log.Warn().Err(err).Int("partIndex", partIndex).Int("partialLen", len(partBody)).Msg("Read partial part body")
			} else {
				e.log.Debug().Err(err).Int("partIndex", partIndex).Msg("Failed to read part body")
				continue
			}
		}

		if int64(len(partBody)) > maxPartSize {
			partBody = partBody[:maxPartSize]
			result.Truncated = true
			e.log.Warn().Int("partIndex", partIndex).Int64("maxSize", maxPartSize).Msg("Part body truncated")
		}

		e.log.Debug().Int("partIndex", partIndex).Int("partBodyLen", len(partBody)).Msg("Read part body successfully")

		charset := params["charset"]
		if charset == "" && contentType == "text/html" {
			charset = extractCharsetFromHTML(partBody)
		}
		decodedContent := decodeCharset(partBody, charset)

		switch contentType {
		case "text/plain":
			if result.BodyText == "" {
				result.BodyText = decodedContent
			}
		case "text/html":
			if result.BodyHTML == "" {
				result.BodyHTML = decodedContent
			}
		}
	}
}

// parseSinglePartBody parses a single-part message body
func (e *Engine) parseSinglePartBody(entity *gomessage.Entity, result *ParsedBody, messageID string) {
	contentType, params, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
	e.log.Debug().Str("contentType", contentType).Str("charset", params["charset"]).Msg("Processing single-part message")

	// Check for invalid Content-Transfer-Encoding
	cte := strings.ToLower(strings.TrimSpace(entity.Header.Get("Content-Transfer-Encoding")))
	if cte != "" && cte != "7bit" && cte != "8bit" && cte != "binary" &&
		cte != "quoted-printable" && cte != "base64" {
		e.log.Warn().Str("cte", cte).Msg("Non-standard Content-Transfer-Encoding in single-part, marking unsafe")
		result.UnsafeContent = true
		result.BodyText = "This message uses non-standard encoding and cannot be displayed safely."
		return
	}

	// A single-part body can itself be an attachment (e.g. a DMARC report whose whole
	// body is application/zip). Mirror parseMultipartBody's classification so it becomes a
	// downloadable attachment instead of raw text dumped into the body.
	disposition, dispParams, _ := mime.ParseMediaType(entity.Header.Get("Content-Disposition"))
	contentID := strings.Trim(entity.Header.Get("Content-ID"), "<>")
	isNonTextBody := contentType != "" && !strings.HasPrefix(contentType, "text/") &&
		!isSignaturePart(contentType)
	if disposition == "attachment" || isNonTextBody {
		result.HasAttachments = true
		isInline := disposition == "inline" || contentID != ""
		att := e.extractAttachmentMetadata(entity, messageID, contentType, dispParams, contentID, isInline)
		if att != nil {
			result.Attachments = append(result.Attachments, att)
		}
		return
	}

	lr := io.LimitReader(entity.Body, maxPartSize+1)
	body, err := io.ReadAll(lr)
	if err != nil {
		e.log.Debug().Err(err).Msg("Failed to read single-part message body")
		return
	}

	if int64(len(body)) > maxPartSize {
		body = body[:maxPartSize]
		result.Truncated = true
		e.log.Warn().Int64("maxSize", maxPartSize).Msg("Single-part body truncated")
	}

	e.log.Debug().Int("bodyLen", len(body)).Msg("Read single-part message body")

	charset := params["charset"]
	if charset == "" && contentType == "text/html" {
		charset = extractCharsetFromHTML(body)
	}
	decodedContent := decodeCharset(body, charset)

	e.log.Debug().Int("decodedLen", len(decodedContent)).Msg("Decoded single-part content")

	switch contentType {
	case "text/html":
		result.BodyHTML = decodedContent
	default:
		result.BodyText = decodedContent
	}
}

// parseMessageBody parses a raw email message and extracts text/plain and text/html parts.
// This is the legacy parsing path used by buildMessageFromStreamedData (via FetchServerMessage).
// It does not handle S/MIME or PGP detection - see parseMessageBodyInternal for the modern path.
// truncated reports whether any part was cut at the 10 MB cap (finding 26);
// the returned bodies are then only the first part of the message.
func (e *Engine) parseMessageBody(raw []byte) (bodyText, bodyHTML string, hasAttachments, truncated bool) {
	reader := bytes.NewReader(raw)

	// Parse the message using go-message
	entity, err := gomessage.Read(reader)
	if err != nil {
		e.log.Debug().Err(err).Int("rawLen", len(raw)).Msg("Failed to parse message, trying as plain text")
		// If parsing fails, treat entire content as plain text
		return string(raw), "", false, false
	}

	// Log top-level Content-Type for debugging
	topLevelCT := entity.Header.Get("Content-Type")
	e.log.Debug().
		Str("topLevelContentType", topLevelCT).
		Int("rawLen", len(raw)).
		Msg("Parsing message body")

	// Check if it's a multipart message
	mr := entity.MultipartReader()
	e.log.Debug().Bool("isMultipart", mr != nil).Msg("Multipart detection result")

	if mr != nil {
		// Multipart message - iterate through parts
		partIndex := 0
		for {
			part, err := mr.NextPart()
			if err != nil {
				// EOF (or wrapped EOF like "multipart: NextPart: EOF") signals end of parts
				if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "EOF") {
					e.log.Debug().Err(err).Int("partsProcessed", partIndex).Msg("Error reading multipart")
				} else {
					e.log.Debug().Int("partsProcessed", partIndex).Msg("Finished reading multipart parts")
				}
				break
			}
			partIndex++

			contentType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			disposition, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

			e.log.Debug().
				Int("partIndex", partIndex).
				Str("contentType", contentType).
				Str("disposition", disposition).
				Str("charset", params["charset"]).
				Msg("Processing multipart part")

			// Check for attachments (signature parts are not attachments)
			if disposition == "attachment" && !isSignaturePart(contentType) {
				hasAttachments = true
				continue
			}

			// Handle nested multipart
			if strings.HasPrefix(contentType, "multipart/") {
				nestedText, nestedHTML, nestedAttach, nestedTrunc := e.parseNestedMultipart(part)
				if bodyText == "" {
					bodyText = nestedText
				}
				if bodyHTML == "" {
					bodyHTML = nestedHTML
				}
				hasAttachments = hasAttachments || nestedAttach
				truncated = truncated || nestedTrunc
				continue
			}

			// Read the part body with size limit to prevent memory exhaustion
			// (maxPartSize+1 so an exactly-at-cap part is not misread as truncated)
			lr := io.LimitReader(part.Body, maxPartSize+1)
			partBody, err := io.ReadAll(lr)
			if err != nil {
				// Check if we got partial data despite the error (e.g., malformed email missing closing boundary)
				if len(partBody) > 0 {
					e.log.Warn().
						Err(err).
						Int("partIndex", partIndex).
						Int("partialLen", len(partBody)).
						Msg("Read partial part body despite error, using partial data")
					// Continue processing with partial data (don't skip)
				} else {
					e.log.Debug().Err(err).Int("partIndex", partIndex).Msg("Failed to read part body, no data recovered")
					continue
				}
			}

			// Past the size limit: keep only the first maxPartSize bytes
			// and flag the message truncated (finding 26).
			if int64(len(partBody)) > maxPartSize {
				partBody = partBody[:maxPartSize]
				truncated = true
				e.log.Warn().
					Int("partIndex", partIndex).
					Int64("maxSize", maxPartSize).
					Msg("Part body truncated at size limit - saving partial content")
			}

			e.log.Debug().
				Int("partIndex", partIndex).
				Int("partBodyLen", len(partBody)).
				Msg("Read part body successfully")

			// First, check if content needs explicit quoted-printable decoding
			// (go-message should handle this via Entity.Body, but some edge cases might slip through)
			partBody = decodeQuotedPrintableIfNeeded(partBody)

			// Decode charset to UTF-8
			charset := params["charset"]

			// If no charset in header and this is HTML, try to extract from meta tags
			if charset == "" && contentType == "text/html" {
				charset = extractCharsetFromHTML(partBody)
				e.log.Debug().
					Str("charsetFromHTML", charset).
					Msg("Extracted charset from HTML meta tags")
			}
			decodedContent := decodeCharset(partBody, charset)

			// Debug: Check if content still contains quoted-printable sequences
			if contentType == "text/html" && len(decodedContent) > 200 {
				snippet := decodedContent
				if len(snippet) > 200 {
					snippet = snippet[:200]
				}
				e.log.Debug().
					Str("htmlSnippet", snippet).
					Bool("hasQuotedPrintable", strings.Contains(decodedContent, "=3D")).
					Msg("HTML content analysis")
			}

			switch contentType {
			case "text/plain":
				if bodyText == "" {
					bodyText = decodedContent
				}
			case "text/html":
				if bodyHTML == "" {
					bodyHTML = decodedContent
				}
			default:
				// Other content types might be inline attachments
				if disposition == "inline" && strings.HasPrefix(contentType, "image/") {
					// Inline images need to be extracted so they can be displayed
					hasAttachments = true
				} else if contentType != "" && !strings.HasPrefix(contentType, "text/") && !isSignaturePart(contentType) {
					hasAttachments = true
				}
			}
		}
	} else {
		// Single part message
		contentType, params, _ := mime.ParseMediaType(entity.Header.Get("Content-Type"))
		e.log.Debug().
			Str("contentType", contentType).
			Str("charset", params["charset"]).
			Msg("Processing single-part message")

		// A single-part body can itself be an attachment (e.g. a DMARC
		// report whose whole body is application/zip) — mirror the modern
		// parseSinglePartBody classification. Without this the flag stays
		// false and FetchServerMessage never runs the extractor, so a
		// single-part attachment opened from server search gets no
		// downloadable rows. Signature parts are excluded, and the raw
		// attachment bytes are not dumped into the body text.
		disposition, _, _ := mime.ParseMediaType(entity.Header.Get("Content-Disposition"))
		isNonTextBody := contentType != "" && !strings.HasPrefix(contentType, "text/") &&
			!isSignaturePart(contentType)
		if disposition == "attachment" || isNonTextBody {
			hasAttachments = true
			return "", "", true
		}

		// Read with size limit to prevent memory exhaustion
		// (maxPartSize+1 so an exactly-at-cap body is not misread as truncated)
		lr := io.LimitReader(entity.Body, maxPartSize+1)
		body, err := io.ReadAll(lr)
		if err != nil {
			e.log.Debug().Err(err).Msg("Failed to read single-part message body")
			return "", "", false, false
		}

		// Past the size limit: keep only the first maxPartSize bytes
		// and flag the message truncated (finding 26).
		if int64(len(body)) > maxPartSize {
			body = body[:maxPartSize]
			truncated = true
			e.log.Warn().
				Int64("maxSize", maxPartSize).
				Msg("Single-part body truncated at size limit - saving partial content")
		}

		e.log.Debug().Int("bodyLen", len(body)).Msg("Read single-part message body")

		// First, check if content needs explicit quoted-printable decoding
		body = decodeQuotedPrintableIfNeeded(body)

		// Decode charset to UTF-8
		charset := params["charset"]
		// If no charset in header and this is HTML, try to extract from meta tags
		if charset == "" && contentType == "text/html" {
			charset = extractCharsetFromHTML(body)
		}
		decodedContent := decodeCharset(body, charset)

		e.log.Debug().Int("decodedLen", len(decodedContent)).Msg("Decoded single-part content")

		switch contentType {
		case "text/html":
			bodyHTML = decodedContent
		default:
			// Default to plain text
			bodyText = decodedContent
		}
	}

	// Log final result
	e.log.Debug().
		Int("bodyTextLen", len(bodyText)).
		Int("bodyHTMLLen", len(bodyHTML)).
		Bool("hasAttachments", hasAttachments).
		Msg("parseMessageBody complete")

	return bodyText, bodyHTML, hasAttachments, truncated
}

// parseNestedMultipart handles nested multipart structures.
// This is part of the legacy parsing path used by parseMessageBody.
func (e *Engine) parseNestedMultipart(entity *gomessage.Entity) (bodyText, bodyHTML string, hasAttachments, truncated bool) {
	mr := entity.MultipartReader()
	if mr == nil {
		return "", "", false, false
	}

	for {
		part, err := mr.NextPart()
		if err != nil {
			// EOF (or wrapped EOF) signals end of parts - no need to log
			break
		}

		contentType, params, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		disposition, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

		// Signature parts are not attachments.
		if disposition == "attachment" && !isSignaturePart(contentType) {
			hasAttachments = true
			continue
		}

		if strings.HasPrefix(contentType, "multipart/") {
			nestedText, nestedHTML, nestedAttach, nestedTrunc := e.parseNestedMultipart(part)
			if bodyText == "" {
				bodyText = nestedText
			}
			if bodyHTML == "" {
				bodyHTML = nestedHTML
			}
			hasAttachments = hasAttachments || nestedAttach
			truncated = truncated || nestedTrunc
			continue
		}

		// Read with size limit to prevent memory exhaustion
		// (maxPartSize+1 so an exactly-at-cap part is not misread as truncated)
		lr := io.LimitReader(part.Body, maxPartSize+1)
		partBody, err := io.ReadAll(lr)
		if err != nil {
			// Check if we got partial data despite the error (e.g., malformed email missing closing boundary)
			if len(partBody) > 0 {
				e.log.Warn().
					Err(err).
					Int("partialLen", len(partBody)).
					Msg("Read partial nested part body despite error, using partial data")
				// Continue processing with partial data (don't skip)
			} else {
				continue
			}
		}

		// Past the size limit: keep only the first maxPartSize bytes
		// and flag the message truncated (finding 26).
		if int64(len(partBody)) > maxPartSize {
			partBody = partBody[:maxPartSize]
			truncated = true
			e.log.Warn().
				Int64("maxSize", maxPartSize).
				Msg("Nested part body truncated at size limit - saving partial content")
		}

		// Decode charset to UTF-8
		charset := params["charset"]
		// If no charset in header and this is HTML, try to extract from meta tags
		if charset == "" && contentType == "text/html" {
			charset = extractCharsetFromHTML(partBody)
		}
		decodedContent := decodeCharset(partBody, charset)

		switch contentType {
		case "text/plain":
			if bodyText == "" {
				bodyText = decodedContent
			}
		case "text/html":
			if bodyHTML == "" {
				bodyHTML = decodedContent
			}
		}
	}

	return bodyText, bodyHTML, hasAttachments, truncated
}

// keepWriter counts every byte written while keeping only the first
// limit bytes. Used by drainPart so an attachment's true decoded size is
// recorded even when only a capped prefix is stored (finding 26).
type keepWriter struct {
	buf   bytes.Buffer
	limit int64
	total int64
}

func (w *keepWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += int64(n)
	if int64(w.buf.Len()) < w.limit {
		room := w.limit - int64(w.buf.Len())
		if int64(n) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

// drainPart reads a MIME part body fully, returning the first keep bytes
// and the true decoded size. The old code read through a 10 MB
// LimitReader, so any larger attachment was recorded at exactly 10 MB
// (finding 26). The decoded stream is bounded by the 50 MB message cap
// upstream, so draining cannot run away.
func drainPart(r io.Reader, keep int64) (kept []byte, total int64, err error) {
	w := &keepWriter{limit: keep}
	_, err = io.Copy(w, r)
	return w.buf.Bytes(), w.total, err
}

// extractAttachmentMetadata extracts attachment metadata from a MIME part.
// For inline images, also captures content (up to maxInlineContentSize).
// For file attachments, reads content to get size but doesn't store it (fetched on-demand).
func (e *Engine) extractAttachmentMetadata(part *gomessage.Entity, messageID, contentType string, dispParams map[string]string, contentID string, isInline bool) *message.Attachment {
	filename := dispParams["filename"]
	if filename == "" {
		// Try to get from Content-Type name parameter
		_, ctParams, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		filename = ctParams["name"]
	}
	// Decode RFC 2047 encoded filenames (e.g., =?UTF-8?B?5Lit5paH?= for Chinese)
	filename = decodeMIMEWord(filename)
	if filename == "" {
		// Shared synthesis — the downloader later re-locates the part by
		// this exact name, so all fallback sites must agree (#370)
		filename = email.FallbackFilename(contentType)
	}

	att := &message.Attachment{
		ID:          generateID(),
		MessageID:   messageID,
		Filename:    filename,
		ContentType: contentType,
		ContentID:   contentID,
		IsInline:    isInline,
	}

	// Read the attachment content, counting the true decoded size.
	// Only inline images keep content (capped at maxInlineContentSize);
	// file attachments are fetched on-demand, so only their size is
	// stored — but it must be the real size, not the first 10 MB of it.
	var keep int64
	if isInline {
		keep = maxInlineContentSize
	}
	content, total, err := drainPart(part.Body, keep)
	if err != nil {
		e.log.Debug().Err(err).Str("filename", filename).Msg("Failed to read attachment content")
		return att
	}

	att.Size = int(total)

	if isInline {
		// For inline images, store content (needed for display in email)
		// But limit to maxInlineContentSize to prevent huge DB entries
		if total <= maxInlineContentSize {
			att.Content = content
			e.log.Debug().Str("filename", filename).Int("size", int(total)).Msg("Extracted inline attachment with content")
		} else {
			e.log.Debug().Str("filename", filename).Int("size", int(total)).Msg("Inline attachment too large, stored metadata only")
		}
	} else {
		// For file attachments, we have the size but don't store content
		// Content will be fetched on-demand when user downloads
		e.log.Debug().Str("filename", filename).Int("size", int(total)).Msg("Extracted file attachment metadata")
	}

	return att
}

// isSignaturePart returns true for S/MIME and PGP signature content types
// that should not be exposed as downloadable attachments.
func isSignaturePart(contentType string) bool {
	switch contentType {
	case "application/pkcs7-signature", "application/x-pkcs7-signature",
		"application/pgp-signature":
		return true
	}
	return false
}

// extractPlainTextFallback attempts to extract readable text from raw email bytes.
// Used when normal parsing times out or fails completely.
// Returns partial content which is better than nothing.
func (e *Engine) extractPlainTextFallback(raw []byte) string {
	rawStr := string(raw)

	// Find the body (after double CRLF or double LF - standard email header/body separator)
	bodyStart := strings.Index(rawStr, "\r\n\r\n")
	if bodyStart == -1 {
		bodyStart = strings.Index(rawStr, "\n\n")
	}
	if bodyStart == -1 {
		// No header/body separator found, can't extract safely
		return ""
	}

	body := rawStr[bodyStart+4:]

	// Extract printable ASCII characters as a last resort
	// This handles cases where content might be partially encoded
	var result strings.Builder
	for _, r := range body {
		if r >= 32 && r < 127 || r == '\n' || r == '\r' || r == '\t' {
			result.WriteRune(r)
		}
	}

	text := strings.TrimSpace(result.String())

	// Limit to first 10KB to prevent huge partial extractions
	const maxFallbackSize = 10 * 1024
	if len(text) > maxFallbackSize {
		text = text[:maxFallbackSize] + "... [truncated - parsing timed out]"
	}

	if text != "" {
		e.log.Info().
			Int("extractedLen", len(text)).
			Msg("Extracted partial text via fallback")
	}

	return text
}

/*
// parseMessageBodyWithTimeout parses message body with a timeout.
// If parsing takes too long (potentially due to malformed emails), it returns
// partial results via fallback extraction - better than nothing.
//
// UNUSED: This function is not called anywhere. parseMessageBodyFull provides the
// same functionality with a richer return type (ParsedBody instead of tuple).
func (e *Engine) parseMessageBodyWithTimeout(raw []byte, timeout time.Duration) (bodyText, bodyHTML string, hasAttachments bool) {
	result := e.parseMessageBodyFull(raw, "", timeout)
	return result.BodyText, result.BodyHTML, result.HasAttachments
}
*/
