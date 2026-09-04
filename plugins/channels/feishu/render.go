package feishu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/goldmark/feishucard"
)

// textContent builds the JSON content string for a Feishu text message.
func textContent(text string) string {
	data, _ := json.Marshal(map[string]string{"text": text})
	return string(data)
}

var (
	cardButtonDirective = regexp.MustCompile(`\{\{button\s+([^}]*)\}\}`)
	cardButtonAttr      = regexp.MustCompile(`(\w+)="([^"]*)"`)
)

const (
	// Feishu accepts card message bodies up to 30 KiB. Keep headroom for the
	// SDK envelope and future card fields instead of operating on the edge.
	maxFeishuCardBytes    = 28_000
	maxFeishuCardElements = 200
	maxFeishuCardTables   = 5
	maxFeishuCardSummary  = 120
)

var errFeishuCardLimit = errors.New("feishu card exceeds delivery limits")

type cardStatus string

const (
	cardStatusRunning   cardStatus = "running"
	cardStatusCompleted cardStatus = "completed"
	cardStatusFailed    cardStatus = "failed"
)

// stripCardDirectives rewrites {{button ...}} directives into readable plain
// text ("label: url") so a plain-text fallback never shows raw directives. Used
// when an interactive card can't be built and the reply must degrade to text.
func stripCardDirectives(text string) string {
	return cardButtonDirective.ReplaceAllStringFunc(text, func(m string) string {
		attrs := map[string]string{}
		for _, a := range cardButtonAttr.FindAllStringSubmatch(m, -1) {
			attrs[a[1]] = a[2]
		}
		switch {
		case attrs["label"] != "" && attrs["url"] != "":
			return attrs["label"] + ": " + attrs["url"]
		case attrs["url"] != "":
			return attrs["url"]
		default:
			return strings.TrimSpace(attrs["label"])
		}
	})
}

var buildCardContent = func(text string) (string, error) {
	return buildCardContentForStatus(text, cardStatusCompleted)
}

func buildCardContentForStatus(text string, status cardStatus) (string, error) {
	elements := feishucard.Render(text)
	assignCardElementIDs(elements, "content")
	title, subtitle, template := cardStatusPresentation(status)
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
			"summary": map[string]any{
				"content": cardSummary(text),
			},
		},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
			"subtitle": map[string]any{
				"tag":     "plain_text",
				"content": subtitle,
			},
		},
		"body": map[string]any{
			"elements": elements,
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	if err := validateCardLimits(card, len(data)); err != nil {
		return "", err
	}
	return string(data), nil
}

func cardStatusPresentation(status cardStatus) (title, subtitle, template string) {
	switch status {
	case cardStatusRunning:
		return "Stella", "Generating response", "blue"
	case cardStatusFailed:
		return "Stella", "Response failed", "red"
	default:
		return "Stella", "Completed", "green"
	}
}

func cardSummary(text string) string {
	summary := strings.Join(strings.Fields(stripCardDirectives(text)), " ")
	if summary == "" {
		return "Stella response"
	}
	runes := []rune(summary)
	if len(runes) > maxFeishuCardSummary {
		summary = string(runes[:maxFeishuCardSummary-3]) + "..."
	}
	return summary
}

func assignCardElementIDs(elements []map[string]any, prefix string) {
	for i, element := range elements {
		assignCardElementID(element, fmt.Sprintf("%s_%d", prefix, i))
	}
}

func assignCardElementID(value any, path string) {
	switch value := value.(type) {
	case map[string]any:
		if _, tagged := value["tag"]; tagged {
			if _, exists := value["element_id"]; !exists {
				value["element_id"] = path
			}
		}
		for key, child := range value {
			if key == "element_id" {
				continue
			}
			assignCardElementID(child, path+"_"+key)
		}
	case []map[string]any:
		assignCardElementIDs(value, path)
	case []any:
		for i, child := range value {
			assignCardElementID(child, fmt.Sprintf("%s_%d", path, i))
		}
	}
}

func validateCardLimits(card map[string]any, encodedBytes int) error {
	elements, tables := countCardElements(card)
	switch {
	case encodedBytes > maxFeishuCardBytes:
		return fmt.Errorf("%w: %d bytes exceeds %d", errFeishuCardLimit, encodedBytes, maxFeishuCardBytes)
	case elements > maxFeishuCardElements:
		return fmt.Errorf("%w: %d elements exceeds %d", errFeishuCardLimit, elements, maxFeishuCardElements)
	case tables > maxFeishuCardTables:
		return fmt.Errorf("%w: %d tables exceeds %d", errFeishuCardLimit, tables, maxFeishuCardTables)
	default:
		return nil
	}
}

func countCardElements(value any) (elements, tables int) {
	switch value := value.(type) {
	case map[string]any:
		if tag, ok := value["tag"].(string); ok {
			elements++
			if tag == "table" {
				tables++
			}
		}
		for _, child := range value {
			childElements, childTables := countCardElements(child)
			elements += childElements
			tables += childTables
		}
	case []map[string]any:
		for _, child := range value {
			childElements, childTables := countCardElements(child)
			elements += childElements
			tables += childTables
		}
	case []any:
		for _, child := range value {
			childElements, childTables := countCardElements(child)
			elements += childElements
			tables += childTables
		}
	}
	return elements, tables
}

// cardContent builds a Feishu Interactive Card JSON 2.0 string.
// GFM tables become native table components; all other content is rendered
// as markdown elements. Cards support the Patch API for in-place editing.
func cardContent(text string) string {
	content, err := buildCardContent(text)
	if err != nil {
		return textContent(text)
	}
	return content
}

// feishuFileType maps common file extensions to Feishu's file_type field.
// Unrecognised extensions fall back to "stream" (generic binary).
func feishuFileType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return "mp4"
	case ".opus", ".mp3", ".wav", ".aac", ".amr", ".ogg":
		return "opus"
	default:
		return "stream"
	}
}

// feishuMessageTypeForFile chooses Feishu's native player messages when the
// stream supplies a recognised audio or video file. Every other extension
// keeps the generic file message behavior.
func feishuMessageTypeForFile(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".opus", ".mp3", ".wav", ".aac", ".amr", ".ogg":
		return "audio"
	case ".mp4", ".mov", ".avi", ".mkv", ".webm":
		return "media"
	default:
		return larkim.MsgTypeFile
	}
}

// sendFile uploads a local file to Feishu and sends it as a file message reply.
func (b *Bot) sendFile(chatID, replyMsgID string, file channel.FileEvent, replyInThread bool) error {
	name := file.Name
	if name == "" {
		name = filepath.Base(file.Path)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	var fileKey string
	err := b.retryFeishuSend(uploadCtx, "upload file", func(ctx context.Context) error {
		f, openErr := os.Open(file.Path)
		if openErr != nil {
			return fmt.Errorf("open file %q: %w", file.Path, openErr)
		}
		defer func() { _ = f.Close() }()
		uploadResp, uploadErr := b.client.Im.File.Create(ctx,
			larkim.NewCreateFileReqBuilder().
				Body(larkim.NewCreateFileReqBodyBuilder().
					FileType(feishuFileType(name)).
					FileName(name).
					File(f).
					Build()).
				Build())
		if uploadErr != nil {
			return uploadErr
		}
		if !uploadResp.Success() {
			return newFeishuAPIError(uploadResp.Code, uploadResp.Msg, uploadResp.Header)
		}
		if uploadResp.Data == nil || uploadResp.Data.FileKey == nil {
			return fmt.Errorf("upload file %q: no file_key returned", name)
		}
		fileKey = *uploadResp.Data.FileKey
		return nil
	})
	if err != nil {
		return fmt.Errorf("upload file %q: %w", name, err)
	}
	if fileKey == "" {
		return fmt.Errorf("upload file %q: no file_key returned", name)
	}

	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	if err := b.retryFeishuSend(replyCtx, "send file", func(ctx context.Context) error {
		resp, replyErr := b.client.Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(replyMsgID).
				Body(replyMessageBody(feishuMessageTypeForFile(name), string(content), replyInThread)).
				Build())
		if replyErr != nil {
			return replyErr
		}
		if !resp.Success() {
			return newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("send file %q: %w", name, err)
	}
	return nil
}

// sendImage decodes a base64 image, uploads it to Feishu to obtain an image_key,
// then sends it as an image message in the chat.
func (b *Bot) sendImage(chatID, replyMsgID string, img channel.ImageEvent, replyInThread bool) error {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}

	uploadCtx, cancelUpload := b.apiContext()
	defer cancelUpload()

	var imageKey string
	if err := b.retryFeishuSend(uploadCtx, "upload image", func(ctx context.Context) error {
		uploadResp, uploadErr := b.client.Im.Image.Create(ctx,
			larkim.NewCreateImageReqBuilder().
				Body(larkim.NewCreateImageReqBodyBuilder().
					ImageType("message").
					Image(bytes.NewReader(data)).
					Build()).
				Build())
		if uploadErr != nil {
			return uploadErr
		}
		if !uploadResp.Success() {
			return newFeishuAPIError(uploadResp.Code, uploadResp.Msg, uploadResp.Header)
		}
		if uploadResp.Data == nil || uploadResp.Data.ImageKey == nil {
			return fmt.Errorf("upload image: no image_key returned")
		}
		imageKey = *uploadResp.Data.ImageKey
		return nil
	}); err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	if imageKey == "" {
		return fmt.Errorf("upload image: no image_key returned")
	}

	// Send image message as a reply.
	replyCtx, cancelReply := b.apiContext()
	defer cancelReply()

	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	if err := b.retryFeishuSend(replyCtx, "send image", func(ctx context.Context) error {
		resp, replyErr := b.client.Im.Message.Reply(ctx,
			larkim.NewReplyMessageReqBuilder().
				MessageId(replyMsgID).
				Body(replyMessageBody(larkim.MsgTypeImage, string(content), replyInThread)).
				Build())
		if replyErr != nil {
			return replyErr
		}
		if !resp.Success() {
			return newFeishuAPIError(resp.Code, resp.Msg, resp.Header)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("send image: %w", err)
	}
	return nil
}
