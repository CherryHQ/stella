package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

const feishuSlashCommandPath = "/open-apis/application/v7/app_slash_commands"

type nativeSlashCommand struct {
	Name          string
	Description   string
	DescriptionZH string
}

var feishuNativeSlashCommands = []nativeSlashCommand{
	{Name: "help", Description: "Show Stella's help message.", DescriptionZH: "查看 Stella 帮助。"},
	{Name: "start", Description: "Show Stella's welcome message.", DescriptionZH: "查看 Stella 欢迎信息。"},
	{Name: "new", Description: "Start a new Stella session in this chat.", DescriptionZH: "在当前会话中开始新的 Stella 会话。"},
	{Name: "compact", Description: "Compact the current Stella session.", DescriptionZH: "压缩当前 Stella 会话。"},
	{Name: "abort", Description: "Abort Stella's active response in this chat.", DescriptionZH: "终止 Stella 在当前会话中的回复。"},
	{Name: "whoami", Description: "Show your Feishu user ID.", DescriptionZH: "查看你的飞书用户 ID。"},
	{Name: "link", Description: "Link this Feishu account to a Stella user.", DescriptionZH: "将此飞书账号关联到 Stella 用户。"},
}

type remoteSlashCommand struct {
	Name string
}

type slashCommandAPI interface {
	List(context.Context) ([]remoteSlashCommand, error)
	Create(context.Context, nativeSlashCommand) error
}

type larkSlashCommandAPI struct {
	client *lark.Client
}

func (a larkSlashCommandAPI) List(ctx context.Context) ([]remoteSlashCommand, error) {
	var result struct {
		Items []struct {
			Name string `json:"command"`
		} `json:"items"`
	}
	if err := a.do(ctx, http.MethodGet, feishuSlashCommandPath, nil, &result); err != nil {
		return nil, err
	}
	commands := make([]remoteSlashCommand, 0, len(result.Items))
	for _, item := range result.Items {
		commands = append(commands, remoteSlashCommand{Name: item.Name})
	}
	return commands, nil
}

func (a larkSlashCommandAPI) Create(ctx context.Context, command nativeSlashCommand) error {
	return a.do(ctx, http.MethodPost, feishuSlashCommandPath, slashCommandBody(command), nil)
}

func (a larkSlashCommandAPI) do(ctx context.Context, method, path string, body any, out any) error {
	resp, err := a.client.Do(ctx, &larkcore.ApiReq{
		HttpMethod:                method,
		ApiPath:                   path,
		Body:                      body,
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return err
	}
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &envelope); err != nil {
		return fmt.Errorf("parse slash command response: %w", err)
	}
	if envelope.Code != 0 {
		return fmt.Errorf("slash command API error (code=%d): %s", envelope.Code, envelope.Msg)
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("parse slash command data: %w", err)
		}
	}
	return nil
}

func slashCommandBody(command nativeSlashCommand) map[string]any {
	return map[string]any{
		"command": command.Name,
		"description": map[string]any{
			"default_value": command.Description,
			"i18n": map[string]string{
				"en_us": command.Description,
				"zh_cn": command.DescriptionZH,
			},
		},
	}
}

// registerNativeCommands is best-effort. Typed commands remain available when
// an existing manually configured app has not granted command management scopes.
func (b *Bot) registerNativeCommands(ctx context.Context) {
	if b.slashCommands == nil {
		return
	}
	existing, err := b.slashCommands.List(ctx)
	if err != nil {
		logger().Warn("register feishu native slash commands failed; typed commands remain available", "error", err)
		return
	}
	byName := make(map[string]struct{}, len(existing))
	for _, command := range existing {
		byName[command.Name] = struct{}{}
	}
	for _, command := range feishuNativeSlashCommands {
		if _, ok := byName[command.Name]; ok {
			continue
		}
		if err := b.slashCommands.Create(ctx, command); err != nil {
			logger().Warn("create feishu native slash command failed", "command", command.Name, "error", err)
		}
	}
}
