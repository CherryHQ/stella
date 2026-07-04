package domaintools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/credentials"
	emailpkg "github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/toolctx"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	defaultToolPageSize = 20
	maxToolPageSize     = 100
)

func truncateText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[:max], true
}

type GoalTool struct {
	svc *goal.Service
}

func NewGoalTool(svc *goal.Service) *GoalTool {
	return &GoalTool{svc: svc}
}

func (t *GoalTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "goal",
		Description: "Manage durable background goals for work that must survive turns, decompose into accepted subwork, or be cancelled later. Actions: create a goal, list goals, get status, cancel. Use scheduler for recurring or future timed prompts, not goal.",
		InputSchema: GoalInputSchema(),
	}
}

func (t *GoalTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("goal service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "goal")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "goal")
	if err != nil {
		return "", err
	}
	out, err := DispatchGoal(ctx, goalHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("goal", err)
	}
	return marshalToolResult(out)
}

type goalHandler struct {
	svc   *goal.Service
	ident toolctx.Identity
}

func (h goalHandler) Create(ctx context.Context, in GoalCreateInput) (any, error) {
	create := goal.CreateInput{
		AgentID: h.ident.AgentID,
		Title:   in.Title,
		Intent:  in.Intent,
		Kind:    goal.KindComposite,
	}
	if in.ProjectId != "" {
		create.ProjectID = in.ProjectId
	}
	if in.Priority != "" {
		create.Priority = in.Priority
	}
	if in.ReviewPolicy != "" {
		create.ReviewPolicy = in.ReviewPolicy
	}
	if len(in.AcceptanceContract) > 0 {
		if err := decodeMap(in.AcceptanceContract, &create.Contract); err != nil {
			return nil, fmt.Errorf("acceptance_contract is invalid — fix the JSON object and retry")
		}
	}
	if len(in.ConvergencePolicy) > 0 {
		if err := decodeMap(in.ConvergencePolicy, &create.Convergence); err != nil {
			return nil, fmt.Errorf("convergence_policy is invalid — fix the JSON object and retry")
		}
	}
	create.IdempotencyKey = in.IdempotencyKey
	row, err := h.svc.CreateGoalOwned(ctx, h.ident, create)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

func (h goalHandler) Get(ctx context.Context, in GoalGetInput) (any, error) {
	row, err := h.svc.GetGoalOwned(ctx, h.ident, in.Id)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

func (h goalHandler) List(ctx context.Context, in GoalListInput) (any, error) {
	limit, offset, err := parseToolPage(in.PageSize, in.PageToken)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	filter := goal.GoalFilter{
		Lifecycle: in.Lifecycle,
		ProjectID: in.ProjectId,
		Terminal:  in.Terminal,
		Q:         in.Q,
	}
	if in.Archived != nil {
		filter.Archived = *in.Archived
	}
	var rows []sqlc.AgentGoal
	switch {
	case in.Parent != "":
		rows, err = h.svc.ListChildrenOwned(ctx, h.ident, in.Parent)
	case in.Root != "":
		rows, err = h.svc.ListSubtreeOwned(ctx, h.ident, in.Root)
	default:
		rows, err = h.svc.ListGoalsOwned(ctx, h.ident, filter, int64(limit+1), int64(offset))
	}
	if err != nil {
		return nil, err
	}
	page, next := pageRows(rows, limit, offset)
	items := make([]goalResponse, 0, len(page))
	for _, row := range page {
		items = append(items, goalSummary(row))
	}
	return listResponse[goalResponse]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h goalHandler) Cancel(ctx context.Context, in GoalCancelInput) (any, error) {
	if err := h.svc.CancelOwned(ctx, h.ident, in.Id, in.Reason); err != nil {
		return nil, err
	}
	row, err := h.svc.GetGoalOwned(ctx, h.ident, in.Id)
	if err != nil {
		return nil, err
	}
	return goalSummary(row), nil
}

type EmailTool struct {
	svc *emailpkg.Service
}

func NewEmailTool(svc *emailpkg.Service) *EmailTool {
	return &EmailTool{svc: svc}
}

func (t *EmailTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "email",
		Description: "Read configured email accounts, list/read messages, and send mail for this user. Actions: accounts, list, read, send. Send requires idempotency_key; reuse the same key only when retrying the exact same send. Message bodies are truncated for token safety. Never exposes passwords or EMAIL_CONFIG contents.",
		InputSchema: EmailInputSchema(),
	}
}

func (t *EmailTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("email service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "email")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "email")
	if err != nil {
		return "", err
	}
	out, err := DispatchEmail(ctx, emailHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("email", err)
	}
	return marshalToolResult(out)
}

type emailHandler struct {
	svc   *emailpkg.Service
	ident toolctx.Identity
}

func (h emailHandler) Accounts(ctx context.Context, _ EmailAccountsInput) (any, error) {
	accounts, err := h.svc.AccountsOwned(ctx, h.ident)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accounts": accounts.Accounts, "default": accounts.Default}, nil
}

func (h emailHandler) List(ctx context.Context, in EmailListInput) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = defaultToolPageSize
	}
	if limit < 1 || limit > maxToolPageSize {
		return nil, fmt.Errorf("invalid limit — use a value between 1 and %d", maxToolPageSize)
	}
	opts := emailpkg.ListOptions{Limit: limit, Folder: in.Folder, From: in.From, Subject: in.Subject}
	if in.Unread != nil {
		opts.Unread = *in.Unread
	}
	if in.Since != "" {
		if t, err := time.Parse("2006-01-02", in.Since); err == nil {
			opts.Since = &t
		}
	}
	if in.Before != "" {
		if t, err := time.Parse("2006-01-02", in.Before); err == nil {
			opts.Before = &t
		}
	}
	msgs, err := h.svc.ListOwned(ctx, h.ident, in.Account, opts)
	if err != nil {
		return nil, err
	}
	items := make([]emailEnvelopeResponse, 0, len(msgs))
	for _, msg := range msgs {
		items = append(items, emailEnvelopeSummary(msg))
	}
	return listResponse[emailEnvelopeResponse]{Items: items, HasMore: false}, nil
}

func (h emailHandler) Read(ctx context.Context, in EmailReadInput) (any, error) {
	msg, err := h.svc.ReadOwned(ctx, h.ident, in.Account, in.Folder, uint32(in.Uid))
	if err != nil {
		return nil, err
	}
	return emailMessageSummary(msg), nil
}

func (h emailHandler) Send(ctx context.Context, in EmailSendInput) (any, error) {
	opts := emailpkg.SendOptions{To: stringItems(in.To), Cc: stringItems(in.Cc), Bcc: stringItems(in.Bcc), Subject: in.Subject, Body: in.Body, From: in.From, ReplyTo: in.ReplyTo, InReplyTo: in.InReplyTo}
	if in.Html != nil {
		opts.HTML = *in.Html
	}
	result, err := h.svc.SendOwned(ctx, h.ident, in.Account, opts, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if result.Duplicate {
		return map[string]any{"status": result.Status, "duplicate_suppressed": true}, nil
	}
	return map[string]any{"status": result.Status}, nil
}

type OauthTool struct {
	svc *credentials.Service
}

func NewOauthTool(svc *credentials.Service) *OauthTool {
	return &OauthTool{svc: svc}
}

func (t *OauthTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "oauth",
		Description: "Connect and manage external OAuth providers for this user. Actions: list providers, connect, status, disconnect. For connect, give the user the returned verification_uri and user_code, ask them to authorize and tell you when done, then call action=status with the flow_id. Never tell the user to run commands; never expose tokens.",
		InputSchema: OauthInputSchema(),
	}
}

func (t *OauthTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("oauth service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "oauth")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "oauth")
	if err != nil {
		return "", err
	}
	out, err := DispatchOauth(ctx, oauthHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("oauth", err)
	}
	return marshalToolResult(out)
}

type oauthHandler struct {
	svc   *credentials.Service
	ident toolctx.Identity
}

func (h oauthHandler) Connect(ctx context.Context, in OauthConnectInput) (any, error) {
	status, err := h.svc.StartFlowOwned(ctx, h.ident, in.Provider)
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func (h oauthHandler) Status(ctx context.Context, in OauthStatusInput) (any, error) {
	status, _, err := h.svc.PollFlowOwned(ctx, h.ident, in.Provider, in.FlowId)
	if err != nil {
		return nil, err
	}
	return oauthFlowSummary(status), nil
}

func (h oauthHandler) List(ctx context.Context, _ OauthListInput) (any, error) {
	providers, err := h.svc.StatusesOwned(ctx, h.ident)
	if err != nil {
		return nil, err
	}
	items := make([]oauthProviderResponse, 0, len(providers))
	for _, provider := range providers {
		items = append(items, oauthProviderSummary(provider))
	}
	return map[string]any{"providers": items}, nil
}

func (h oauthHandler) Disconnect(ctx context.Context, in OauthDisconnectInput) (any, error) {
	if err := h.svc.DisconnectOwned(ctx, h.ident, in.Provider); err != nil {
		return nil, err
	}
	return map[string]any{"provider": in.Provider, "status": "disconnected"}, nil
}

type RecallyTool struct {
	svc *recally.Service
}

func NewRecallyTool(svc *recally.Service) *RecallyTool {
	return &RecallyTool{svc: svc}
}

func (t *RecallyTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "recally",
		Description: "Save and read the user's Recally library. Actions: save batches fetched article content, list_articles, get_article, feed_add/feed_list/feed_remove, digest. For save, fetch the article content yourself first (for example with web/tap tools) and include markdown content for new articles; content is required for new articles. The library is shared across this user's agents.",
		InputSchema: RecallyInputSchema(),
	}
}

func (t *RecallyTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("recally service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "recally")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "recally")
	if err != nil {
		return "", err
	}
	out, err := DispatchRecally(ctx, recallyHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("recally", err)
	}
	return marshalToolResult(out)
}

type recallyHandler struct {
	svc   *recally.Service
	ident toolctx.Identity
}

func (h recallyHandler) Save(ctx context.Context, in RecallySaveInput) (any, error) {
	results := make([]recallySaveResult, 0, len(in.Items))
	for _, item := range in.Items {
		result := recallySaveResult{URL: item.Url}
		saved, err := h.svc.SaveOwned(ctx, h.ident, recallySaveRequest(item))
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.ID = saved.Article.ID
		if saved.Created {
			result.Status = "created"
		} else {
			result.Status = "updated"
		}
		results = append(results, result)
	}
	return map[string]any{"results": results}, nil
}

func (h recallyHandler) List_articles(ctx context.Context, in RecallyList_articlesInput) (any, error) {
	limit, offset, err := parseToolPage(in.PageSize, in.PageToken)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	if in.CanonicalUrl != "" {
		article, err := h.svc.GetArticleByCanonicalURLOwned(ctx, h.ident, in.CanonicalUrl)
		if err != nil {
			if errors.Is(err, toolctx.ErrNotFound) {
				return listResponse[recallyArticleListItem]{Items: []recallyArticleListItem{}, HasMore: false}, nil
			}
			return nil, err
		}
		return listResponse[recallyArticleListItem]{Items: []recallyArticleListItem{recallyArticleListSummary(*article)}, HasMore: false}, nil
	}
	var articles []recally.Article
	if in.Q != "" {
		articles, err = h.svc.SearchArticlesOwned(ctx, h.ident, in.Q, limit)
	} else {
		articles, err = h.svc.ListArticlesOwned(ctx, h.ident, recally.ArticleFilter{Status: recally.ArticleStatus(in.Status), SourceType: recally.SourceType(in.SourceType), Starred: in.Starred, Limit: limit + 1, Offset: offset})
	}
	if err != nil {
		return nil, err
	}
	page, next := pageRows(articles, limit, offset)
	items := make([]recallyArticleListItem, 0, len(page))
	for _, article := range page {
		items = append(items, recallyArticleListSummary(article))
	}
	return listResponse[recallyArticleListItem]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h recallyHandler) Get_article(ctx context.Context, in RecallyGet_articleInput) (any, error) {
	article, err := h.svc.GetArticleOwned(ctx, h.ident, in.Id)
	if err != nil {
		return nil, err
	}
	content, err := h.svc.ReadArticleBody(article)
	if err != nil {
		return nil, err
	}
	content, truncated := truncateText(content, 50*1024)
	return recallyArticleDetail{Article: recallyArticleListSummary(*article), Content: content, Truncated: truncated, Note: recallyTruncationNote(truncated)}, nil
}

func (h recallyHandler) Feed_add(ctx context.Context, in RecallyFeed_addInput) (any, error) {
	feed, err := h.svc.CreateFeedOwned(ctx, h.ident, in.Url, recally.FeedKind(in.Kind), in.Title, nil)
	if err != nil {
		return nil, err
	}
	return recallyFeedSummary(*feed), nil
}

func (h recallyHandler) Feed_list(ctx context.Context, in RecallyFeed_listInput) (any, error) {
	limit, offset, err := parseToolPage(in.PageSize, in.PageToken)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	if in.Url != "" {
		feed, err := h.svc.GetFeedByURLOwned(ctx, h.ident, in.Url)
		if err != nil {
			if errors.Is(err, toolctx.ErrNotFound) {
				return listResponse[recallyFeedItem]{Items: []recallyFeedItem{}, HasMore: false}, nil
			}
			return nil, err
		}
		return listResponse[recallyFeedItem]{Items: []recallyFeedItem{recallyFeedSummary(*feed)}, HasMore: false}, nil
	}
	feeds, err := h.svc.ListFeedsOwned(ctx, h.ident, limit+1, offset)
	if err != nil {
		return nil, err
	}
	page, next := pageRows(feeds, limit, offset)
	items := make([]recallyFeedItem, 0, len(page))
	for _, feed := range page {
		items = append(items, recallyFeedSummary(feed))
	}
	return listResponse[recallyFeedItem]{Items: items, HasMore: next != "", NextPageToken: next}, nil
}

func (h recallyHandler) Feed_remove(ctx context.Context, in RecallyFeed_removeInput) (any, error) {
	if err := h.svc.DeleteFeedOwned(ctx, h.ident, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "removed"}, nil
}

func (h recallyHandler) Digest(ctx context.Context, _ RecallyDigestInput) (any, error) {
	digest, err := h.svc.GetDigestOwned(ctx, h.ident)
	if err != nil {
		return nil, err
	}
	return map[string]any{"date": digest.Date.UTC().Format(time.RFC3339), "text": recallyDigestText(digest)}, nil
}

type ShareTool struct {
	svc *sharepkg.Service
}

func NewShareTool(svc *sharepkg.Service) *ShareTool {
	return &ShareTool{svc: svc}
}

func (t *ShareTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "share",
		Description: "Create and manage public share links for this user's artifacts or saved articles. Actions: artifact shares a file from the current session workspace; article shares a Recally article; list shows existing shares; revoke disables a share. For artifact, use the current session automatically and provide a workspace path. Responses include the public URL; never expose private file content unless the user asked to share it.",
		InputSchema: ShareInputSchema(),
	}
}

func (t *ShareTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("share service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "share")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "share")
	if err != nil {
		return "", err
	}
	out, err := DispatchShare(ctx, shareHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("share", err)
	}
	return marshalToolResult(out)
}

type shareHandler struct {
	svc   *sharepkg.Service
	ident toolctx.Identity
}

func (h shareHandler) Artifact(ctx context.Context, in ShareArtifactInput) (any, error) {
	sessionID := memory.SessionIDFromContext(ctx)
	created, err := h.svc.ShareArtifactOwned(ctx, h.ident, sessionID, in.Path, in.Scope, in.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return shareCreatedSummary(created), nil
}

func (h shareHandler) Article(ctx context.Context, in ShareArticleInput) (any, error) {
	created, err := h.svc.ShareArticleOwned(ctx, h.ident, in.ArticleId, in.ExpiresIn)
	if err != nil {
		return nil, err
	}
	return shareCreatedSummary(created), nil
}

func (h shareHandler) List(ctx context.Context, in ShareListInput) (any, error) {
	limit, offset, err := parseToolPage(in.PageSize, in.PageToken)
	if err != nil {
		return nil, fmt.Errorf("invalid pagination — use page_size between 1 and %d and pass next_page_token unchanged", maxToolPageSize)
	}
	result, err := h.svc.ListOwned(ctx, h.ident, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]shareResponse, 0, len(result.Shares))
	for _, row := range result.Shares {
		items = append(items, shareSummary(row, ""))
	}
	return listResponse[shareResponse]{Items: items, HasMore: result.NextPageToken != "", NextPageToken: result.NextPageToken}, nil
}

func (h shareHandler) Revoke(ctx context.Context, in ShareRevokeInput) (any, error) {
	if err := h.svc.RevokeOwned(ctx, h.ident, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "revoked"}, nil
}

type VaultTool struct {
	svc *vault.Service
}

func NewVaultTool(svc *vault.Service) *VaultTool {
	return &VaultTool{svc: svc}
}

func (t *VaultTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "vault",
		Description: "Store, list, and delete secrets for this user or this user+agent. Secrets are injected into sandbox processes as environment variables at session start; there is deliberately no read-back action, and list returns metadata only. Actions: list, set, delete.",
		InputSchema: VaultInputSchema(),
	}
}

func (t *VaultTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("vault service is unavailable — ask an operator to configure STELLA_VAULT_KEY")
	}
	ident, err := toolIdentity(ctx, "vault")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "vault")
	if err != nil {
		return "", err
	}
	out, err := DispatchVault(ctx, vaultHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("vault", err)
	}
	return marshalToolResult(out)
}

type vaultHandler struct {
	svc   *vault.Service
	ident toolctx.Identity
}

func (h vaultHandler) List(ctx context.Context, in VaultListInput) (any, error) {
	entries, err := h.svc.ListOwned(ctx, h.ident, in.Scope)
	if err != nil {
		return nil, err
	}
	items := make([]vaultResponse, 0, len(entries))
	for _, entry := range entries {
		items = append(items, vaultSummary(entry))
	}
	return listResponse[vaultResponse]{Items: items, HasMore: false}, nil
}

func (h vaultHandler) Set(ctx context.Context, in VaultSetInput) (any, error) {
	meta, err := h.svc.SetOwned(ctx, h.ident, in.Scope, in.Name, in.Value)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": meta.Name, "scope": meta.Scope, "status": "set"}, nil
}

func (h vaultHandler) Delete(ctx context.Context, in VaultDeleteInput) (any, error) {
	if err := h.svc.DeleteOwned(ctx, h.ident, in.Scope, in.Name); err != nil {
		return nil, err
	}
	return map[string]any{"name": in.Name, "scope": defaultVaultScope(in.Scope), "status": "deleted"}, nil
}

func defaultVaultScope(scope string) string {
	if scope == "" {
		return vault.ScopeUser
	}
	return scope
}

type SchedulerTool struct {
	svc *scheduler.Service
}

func NewSchedulerTool(svc *scheduler.Service) *SchedulerTool {
	return &SchedulerTool{svc: svc}
}

func (t *SchedulerTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "scheduler",
		Description: "Manage this agent's scheduled prompts. Actions: create one-time/interval/cron jobs or template subscriptions, list/get/update/delete jobs, pause, resume. Use goal for durable acceptance-tracked work; use scheduler only for when something should run later or repeatedly.",
		InputSchema: SchedulerInputSchema(),
	}
}

func (t *SchedulerTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.svc == nil {
		return "", fmt.Errorf("scheduler service is unavailable — try again later")
	}
	ident, err := toolIdentity(ctx, "scheduler")
	if err != nil {
		return "", err
	}
	action, err := actionArg(args, "scheduler")
	if err != nil {
		return "", err
	}
	out, err := DispatchScheduler(ctx, schedulerHandler{svc: t.svc, ident: ident}, action, args)
	if err != nil {
		return "", mapToolError("scheduler", err)
	}
	return marshalToolResult(out)
}

type schedulerHandler struct {
	svc   *scheduler.Service
	ident toolctx.Identity
}

func (h schedulerHandler) Create(ctx context.Context, in SchedulerCreateInput) (any, error) {
	sched := scheduler.Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
	var job scheduler.Job
	var err error
	if in.TemplateKey != "" {
		job, err = h.svc.SubscribeOwned(ctx, h.ident, h.ident.AgentID, in.TemplateKey, sched)
	} else {
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		job, err = h.svc.CreateJobOwnedWithEnabled(ctx, h.ident, in.Name, in.Message, sched, in.SessionMode, h.ident.AgentID, in.IdempotencyKey, enabled)
	}
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Get(ctx context.Context, in SchedulerGetInput) (any, error) {
	job, err := h.svc.GetJobOwned(ctx, h.ident, h.ident.AgentID, in.Id)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) List(ctx context.Context, _ SchedulerListInput) (any, error) {
	jobs, err := h.svc.ListJobsOwned(ctx, h.ident, h.ident.AgentID)
	if err != nil {
		return nil, err
	}
	items := make([]schedulerResponse, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, schedulerSummary(job))
	}
	return listResponse[schedulerResponse]{Items: items, HasMore: false}, nil
}

func (h schedulerHandler) Update(ctx context.Context, in SchedulerUpdateInput) (any, error) {
	update := scheduler.JobUpdate{}
	if in.Name != "" {
		update.Name = &in.Name
	}
	if in.Message != "" {
		update.Message = &in.Message
	}
	if in.Cron != "" || in.Every != "" || in.At != "" {
		sched := scheduler.Schedule{Cron: in.Cron, Every: in.Every, At: in.At}
		update.Schedule = &sched
	}
	if in.SessionMode != "" {
		update.SessionMode = &in.SessionMode
	}
	if in.Enabled != nil {
		update.Enabled = in.Enabled
	}
	job, err := h.svc.UpdateJobOwned(ctx, h.ident, h.ident.AgentID, in.Id, update)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Delete(ctx context.Context, in SchedulerDeleteInput) (any, error) {
	if err := h.svc.DeleteJobOwned(ctx, h.ident, h.ident.AgentID, in.Id); err != nil {
		return nil, err
	}
	return map[string]any{"id": in.Id, "status": "deleted"}, nil
}

func (h schedulerHandler) Pause(ctx context.Context, in SchedulerPauseInput) (any, error) {
	job, err := h.svc.SetJobEnabledOwned(ctx, h.ident, h.ident.AgentID, in.Id, false)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

func (h schedulerHandler) Resume(ctx context.Context, in SchedulerResumeInput) (any, error) {
	job, err := h.svc.SetJobEnabledOwned(ctx, h.ident, h.ident.AgentID, in.Id, true)
	if err != nil {
		return nil, err
	}
	return schedulerSummary(job), nil
}

type emailEnvelopeResponse struct {
	UID         uint32 `json:"uid"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Date        string `json:"date"`
	Snippet     string `json:"snippet,omitempty"`
	Attachments bool   `json:"has_attachments,omitempty"`
}

type emailMessageResponse struct {
	Envelope  emailEnvelopeResponse `json:"envelope"`
	Body      string                `json:"body"`
	Truncated bool                  `json:"truncated"`
	Note      string                `json:"note,omitempty"`
}

type oauthFlowResponse struct {
	Provider        string `json:"provider"`
	FlowID          string `json:"flow_id"`
	VerificationURI string `json:"verification_uri"`
	UserCode        string `json:"user_code,omitempty"`
	ExpiresAt       string `json:"expires_at"`
	State           string `json:"state"`
}

type oauthProviderResponse struct {
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
	Connected  bool   `json:"connected"`
	Username   string `json:"username,omitempty"`
}

type goalResponse struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Lifecycle       string `json:"lifecycle"`
	AcceptanceState string `json:"acceptance_state"`
	Kind            string `json:"kind"`
	Priority        string `json:"priority"`
	UpdatedAt       string `json:"updated_at"`
}

type recallySaveResult struct {
	URL    string `json:"url"`
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type recallyArticleListItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	SavedAt string `json:"saved_at"`
}

type recallyArticleDetail struct {
	Article   recallyArticleListItem `json:"article"`
	Content   string                 `json:"content"`
	Truncated bool                   `json:"truncated"`
	Note      string                 `json:"note,omitempty"`
}

type recallyFeedItem struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

type shareResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type schedulerResponse struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Enabled     bool          `json:"enabled"`
	Schedule    schedulerView `json:"schedule"`
	SessionMode string        `json:"session_mode"`
	UpdatedAt   string        `json:"updated_at"`
	TemplateKey string        `json:"template_key,omitempty"`
}

type vaultResponse struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	UpdatedAt string `json:"updated_at"`
}

type schedulerView struct {
	Cron  string `json:"cron,omitempty"`
	Every string `json:"every,omitempty"`
	At    string `json:"at,omitempty"`
}

type listResponse[T any] struct {
	Items         []T    `json:"items"`
	HasMore       bool   `json:"has_more"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

func goalSummary(row sqlc.AgentGoal) goalResponse {
	return goalResponse{
		ID:              row.ID,
		Title:           row.Title,
		Lifecycle:       row.Lifecycle,
		AcceptanceState: row.AcceptanceState,
		Kind:            row.Kind,
		Priority:        row.Priority,
		UpdatedAt:       row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func schedulerSummary(job scheduler.Job) schedulerResponse {
	return schedulerResponse{
		ID:          job.ID,
		Name:        job.Name,
		Enabled:     job.Enabled,
		Schedule:    schedulerView{Cron: job.Schedule.Cron, Every: job.Schedule.Every, At: job.Schedule.At},
		SessionMode: job.SessionMode,
		UpdatedAt:   job.UpdatedAt.UTC().Format(time.RFC3339),
		TemplateKey: job.JobKey,
	}
}

func emailEnvelopeSummary(msg emailpkg.Envelope) emailEnvelopeResponse {
	return emailEnvelopeResponse{UID: msg.UID, From: msg.From, Subject: msg.Subject, Date: msg.Date.UTC().Format(time.RFC3339), Attachments: msg.HasAttachments}
}

func emailMessageSummary(msg *emailpkg.Message) emailMessageResponse {
	body := msg.TextBody
	if body == "" {
		body = msg.HTMLBody
	}
	body, truncated := truncateText(body, 50*1024)
	out := emailMessageResponse{Envelope: emailEnvelopeSummary(msg.Envelope), Body: body, Truncated: truncated}
	if truncated {
		out.Note = "truncated — use the web UI or email client for the full message"
	}
	return out
}

func oauthFlowSummary(status credentials.FlowStatus) oauthFlowResponse {
	return oauthFlowResponse{
		Provider:        status.Provider,
		FlowID:          status.FlowID,
		VerificationURI: status.VerificationURI,
		UserCode:        status.UserCode,
		ExpiresAt:       status.ExpiresAt.UTC().Format(time.RFC3339),
		State:           status.State,
	}
}

func oauthProviderSummary(status credentials.ProviderStatus) oauthProviderResponse {
	return oauthProviderResponse{
		Provider:   status.Provider,
		Configured: status.Configured,
		Connected:  status.Connected,
		Username:   status.Username,
	}
}

func recallySaveRequest(item RecallySaveItem) recally.SaveRequest {
	return recally.SaveRequest{
		URL:          item.Url,
		CanonicalURL: item.CanonicalUrl,
		SourceType:   recally.SourceType(item.SourceType),
		Title:        item.Title,
		Author:       item.Author,
		Summary:      item.Summary,
		Tags:         stringItems(item.Tags),
		Content:      item.Content,
		Metadata:     stringMap(item.Metadata),
		PublishedAt:  parseOptionalTime(item.PublishedAt),
	}
}

func recallyArticleListSummary(article recally.Article) recallyArticleListItem {
	return recallyArticleListItem{ID: article.ID, Title: article.Title, URL: article.URL, SavedAt: article.SavedAt.UTC().Format(time.RFC3339)}
}

func recallyFeedSummary(feed recally.Feed) recallyFeedItem {
	return recallyFeedItem{ID: feed.ID, URL: feed.URL, Kind: string(feed.Kind), Title: feed.Title, Enabled: feed.Enabled, UpdatedAt: feed.UpdatedAt.UTC().Format(time.RFC3339)}
}

func recallyTruncationNote(truncated bool) string {
	if truncated {
		return "truncated — use the web UI for the full article"
	}
	return ""
}

func recallyDigestText(d *recally.Digest) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Digest for %s\n", d.Date.UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "Total articles: %d; unread: %d; read: %d; archived: %d; starred: %d\n", d.TotalArticles, d.UnreadCount, d.ReadCount, d.ArchivedCount, d.StarredCount)
	if len(d.SavedYesterday) > 0 {
		b.WriteString("\nSaved yesterday:\n")
		for _, article := range d.SavedYesterday {
			fmt.Fprintf(&b, "- %s — %s\n", article.Title, article.URL)
		}
	}
	if len(d.WorthRevisiting) > 0 {
		b.WriteString("\nWorth revisiting:\n")
		for _, article := range d.WorthRevisiting {
			fmt.Fprintf(&b, "- %s — %s\n", article.Title, article.URL)
		}
	}
	if len(d.TopTags) > 0 {
		b.WriteString("\nTop tags:\n")
		for _, tag := range d.TopTags {
			fmt.Fprintf(&b, "- %s (%d)\n", tag.Tag, tag.Count)
		}
	}
	return b.String()
}

func stringItems(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringMap(items map[string]any) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for k, v := range items {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func parseOptionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func shareCreatedSummary(created sharepkg.Created) shareResponse {
	out := shareResponse{ID: created.Share.ID, URL: created.URL, Title: created.Share.Title, MediaType: created.Share.MediaType, CreatedAt: created.Share.CreatedAt.UTC().Format(time.RFC3339)}
	if created.Share.ExpiresAt.Valid {
		out.ExpiresAt = created.Share.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}

func shareSummary(row sqlc.ListSharesByUserRow, url string) shareResponse {
	out := shareResponse{ID: row.ID, URL: url, Title: row.Title, MediaType: row.MediaType, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339)}
	if row.ExpiresAt.Valid {
		out.ExpiresAt = row.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	return out
}

func vaultSummary(meta vault.EntryMeta) vaultResponse {
	return vaultResponse{Name: meta.Name, Scope: meta.Scope, UpdatedAt: meta.UpdatedAt}
}

func toolIdentity(ctx context.Context, tool string) (toolctx.Identity, error) {
	ident, err := toolctx.FromContext(ctx)
	if err != nil {
		return toolctx.Identity{}, fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	}
	if ident.AgentID == "" {
		return toolctx.Identity{}, fmt.Errorf("this session has no agent identity — %s tools are unavailable here", tool)
	}
	return ident, nil
}

func actionArg(args map[string]any, tool string) (string, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return "", fmt.Errorf("%s action is required — choose an action from the tool schema", tool)
	}
	return action, nil
}

func mapToolError(tool string, err error) error {
	switch {
	case errors.Is(err, toolctx.ErrUnauthenticated):
		return fmt.Errorf("this session has no user identity — %s tools are unavailable here", tool)
	case tool == "oauth" && errors.Is(err, toolctx.ErrNotFound):
		return fmt.Errorf("flow expired or unknown — start a new connect")
	case tool == "share" && errors.Is(err, sharepkg.ErrPathEscapes):
		return fmt.Errorf("path is outside the workspace — choose a file under /workspace or /user and retry")
	case tool == "share" && errors.Is(err, sharepkg.ErrTooLarge):
		return fmt.Errorf("file is too large to share — create a smaller export and retry")
	case tool == "share" && errors.Is(err, sharepkg.ErrUnsupportedType):
		return fmt.Errorf("unsupported artifact type — export as html, markdown, pdf, svg, or an image and retry")
	case errors.Is(err, toolctx.ErrNotFound), errors.Is(err, goal.ErrNotFound):
		return fmt.Errorf("%s not found — check the id with action=list", tool)
	case errors.Is(err, toolctx.ErrForbidden):
		return fmt.Errorf("%s access denied — use action=list to see resources available to this agent", tool)
	default:
		return err
	}
}

func marshalToolResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeMap(src map[string]any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func parseToolPage(pageSize int, pageToken string) (int, int, error) {
	limit := defaultToolPageSize
	if pageSize != 0 {
		if pageSize < 1 || pageSize > maxToolPageSize {
			return 0, 0, fmt.Errorf("invalid page_size")
		}
		limit = pageSize
	}
	if pageToken == "" {
		return limit, 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(pageToken)
	if err != nil {
		return 0, 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid page_token")
	}
	return limit, offset, nil
}

func pageRows[T any](rows []T, limit, offset int) ([]T, string) {
	if len(rows) > limit {
		return rows[:limit], base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
	}
	return rows, ""
}
