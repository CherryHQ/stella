package outbox

import (
	"encoding/json"
	"fmt"
	"strings"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// AddressVersion / PayloadVersion stamp the frozen JSONB value objects. Bump
// on shape change; senders must keep reading older versions.
const (
	AddressVersion = 1
	PayloadVersion = 1
)

// Address fixes where an operation lands: physical chat coordinates plus the
// reply anchor. The platform sender replays it verbatim — it never re-resolves.
type Address struct {
	V          int    `json:"v"`
	ChatKey    string `json:"chat_key"`
	ThreadKey  string `json:"thread_key,omitempty"`
	ReplyToKey string `json:"reply_to_key,omitempty"`
	// Token carries the platform reply credential when one is required —
	// Weixin's context_token authorizes a reply to a conversation.
	Token string `json:"token,omitempty"`
	// Scope marks the platform conversation kind when the chat key alone is
	// ambiguous — QQ needs "group"/"c2c" to pick the API endpoint.
	Scope string `json:"scope,omitempty"`
}

// TextPayload is the frozen body of a send_text op.
type TextPayload struct {
	V    int    `json:"v"`
	Text string `json:"text"`
}

// ReplyOps builds the standard text reply delivery for an inbound event.
// deliveryKey must stay stable across retries so a re-appended completion can
// never double-send. When maxLen > 0 the text is split into one op per chunk,
// each chained on its predecessor so a retried middle chunk never lets a
// later chunk overtake it.
func ReplyOps(runID, deliveryKey, channelID, accountKey string, addr Address, text string, maxLen int) ([]Op, error) {
	a, err := json.Marshal(addr)
	if err != nil {
		return nil, err
	}
	chunks := []string{text}
	if maxLen > 0 {
		chunks = pkgchannel.SplitMessage(text, maxLen)
	}
	ops := make([]Op, 0, len(chunks))
	for i, chunk := range chunks {
		p, err := json.Marshal(TextPayload{V: PayloadVersion, Text: chunk})
		if err != nil {
			return nil, err
		}
		op := Op{
			RunID:       runID,
			DeliveryKey: deliveryKey,
			Index:       i,
			Kind:        OpSendText,
			ChannelID:   channelID,
			AccountKey:  accountKey,
			Address:     a,
			Payload:     p,
		}
		if i > 0 {
			op.DependsOn = []int{i - 1}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// CommandReplyKey names the delivery that carries a command's immediate reply.
// One command event produces exactly one reply op set.
func CommandReplyKey(inboxID string) string { return "reply:" + inboxID }

// DeliveryKeyForRun names the final-reply delivery of a run.
func DeliveryKeyForRun(runID string) string { return "run:" + runID }

// LiveDeliveryKey names the in-progress draft delivery of a run: one
// platform message edited in place while the run executes. Ordered before
// "run:" keys so every live edit dispatches before the terminal reply.
func LiveDeliveryKey(runID string) string { return "live:" + runID }

// DraftOp builds one coalesced progress snapshot op. Index is the next free
// operation_index inside the live delivery; a still-pending predecessor is
// merged in place by the producer instead of appending here.
func DraftOp(runID, channelID, accountKey string, addr Address, seq int64, text string, index int, dependsOn []int) (Op, error) {
	payload, err := json.Marshal(pkgchannel.DraftUpdatePayload{V: PayloadVersion, Seq: seq, Text: text})
	if err != nil {
		return Op{}, err
	}
	rawAddr, err := json.Marshal(addr)
	if err != nil {
		return Op{}, err
	}
	return Op{
		RunID:       runID,
		DeliveryKey: LiveDeliveryKey(runID),
		Index:       index,
		Kind:        OpDraftUpdate,
		ChannelID:   channelID,
		AccountKey:  accountKey,
		Address:     rawAddr,
		Payload:     payload,
		DependsOn:   dependsOn,
	}, nil
}

// ChatKeyFor derives the physical chat coordinate: platform chat id, falling
// back to the sender id for DMs on platforms that leave ChatID empty.
func ChatKeyFor(msg pkgchannel.IncomingMessage) string {
	if msg.ChatID != "" {
		return msg.ChatID
	}
	return msg.SenderID
}

// NotifyPayload is the frozen body of a notify op — the platform-neutral
// Notification an adapter's Notify already renders. Text is the pre-split
// segment this op delivers; a long notification is a chain of ops, one
// platform call each.
type NotifyPayload struct {
	V            int                     `json:"v"`
	Notification pkgchannel.Notification `json:"notification"`
}

// NotifyChain builds a notification delivery for one channel instance: one
// op per text segment, dependency-chained so a retried segment can never be
// overtaken. The delivery key is caller-chosen so a retried notification
// never double-sends.
func NotifyChain(deliveryKey, channelID, accountKey string, n pkgchannel.Notification, maxLen int) ([]Op, error) {
	addr, err := json.Marshal(Address{V: AddressVersion, ChatKey: n.ChatID})
	if err != nil {
		return nil, err
	}
	chunks := splitReplyText(n.Text, maxLen)
	ops := make([]Op, 0, len(chunks))
	for i, chunk := range chunks {
		seg := n
		seg.Text = chunk
		payload, err := json.Marshal(NotifyPayload{V: PayloadVersion, Notification: seg})
		if err != nil {
			return nil, err
		}
		op := Op{
			DeliveryKey: deliveryKey, Index: i, Kind: OpNotify,
			ChannelID: channelID, AccountKey: accountKey, Address: addr, Payload: payload,
		}
		if i > 0 {
			op.DependsOn = []int{i - 1}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// ReplyPayload is the frozen body of a send_reply op; the shared definition
// lives in pkg/channel so adapters decode the same shape.
type ReplyPayload = pkgchannel.ReplyOpPayload

// ReplyPlan describes how a platform's send_reply decomposes into durable
// single-call ops. Every op maps to one platform call, so a mid-chain retry
// can never resend a segment that already landed.
type ReplyPlan struct {
	// TextLimit > 0 splits the flattened reply text into message-sized chunks.
	TextLimit int
	// PrimaryText marks that the send_reply op itself delivers the first text
	// segment (draft-finalize or first message). False means its primary
	// artifact is not a text message — QQ's terminal stream chunk — and every
	// text chunk rides its own send_text op.
	PrimaryText bool
	// Attachments emits one send_attachment op per collected image/file.
	Attachments bool
	// MediaAsText folds collected images/files into explicit text markers
	// instead of attachment ops — for platforms with no binary upload path.
	MediaAsText bool
}

// ReplyChain serializes a completed turn's recorded events into a chain of
// durable single-call ops: send_reply at index 0 for the primary artifact,
// then a send_text op per overflow text chunk, then a send_attachment op per
// image and file. Ops are chained on their predecessor so a retried segment
// never lets a later one overtake it, and each op's receipt is its own row.
func ReplyChain(runID, deliveryKey, channelID, accountKey string, addr Address, sessionID string, events []pkgchannel.Event, plan ReplyPlan) ([]Op, error) {
	rawAddr, err := json.Marshal(addr)
	if err != nil {
		return nil, err
	}
	text, images, files := pkgchannel.CollectReplyEvents(events)
	if plan.MediaAsText {
		text = foldMediaMarkers(text, images, files)
		images, files = nil, nil
	}
	if len([]rune(strings.TrimSpace(text))) == 0 {
		text = "(empty response)"
	}
	chunks := splitReplyText(text, plan.TextLimit)
	primary := ""
	if plan.PrimaryText && len(chunks) > 0 {
		primary = chunks[0]
	}
	payload, err := json.Marshal(ReplyPayload{V: PayloadVersion, SessionID: sessionID, Events: events, Text: primary})
	if err != nil {
		return nil, err
	}
	ops := []Op{{
		RunID: runID, DeliveryKey: deliveryKey, Index: 0, Kind: OpSendReply,
		ChannelID: channelID, AccountKey: accountKey, Address: rawAddr, Payload: payload,
	}}
	start := 0
	if plan.PrimaryText {
		start = 1
	}
	siblings, err := siblingOps(runID, deliveryKey, channelID, accountKey, rawAddr, len(ops), chunks[start:], images, files, plan.Attachments)
	if err != nil {
		return nil, err
	}
	return append(ops, siblings...), nil
}

// GroupReplyChain serializes an accepted group reply into the same per-call
// shape as ReplyChain: send_group_reply at index 0 carries the group metadata
// plus the primary text segment, siblings carry overflow text and media.
func GroupReplyChain(deliveryKey, channelID, accountKey, groupID string, p GroupReplyPayload, events []pkgchannel.Event, plan ReplyPlan) ([]Op, error) {
	for _, evt := range events {
		if evt.Err != nil {
			return nil, evt.Err
		}
	}
	rawAddr, err := json.Marshal(Address{V: AddressVersion, ChatKey: p.PlatformGroupID, ThreadKey: p.PlatformThreadID, ReplyToKey: p.ReplyTo, Scope: "group"})
	if err != nil {
		return nil, err
	}
	text, images, files := pkgchannel.CollectReplyEvents(events)
	if plan.MediaAsText {
		text = foldMediaMarkers(text, images, files)
		images, files = nil, nil
	}
	if len([]rune(strings.TrimSpace(text))) == 0 {
		text = "(empty response)"
	}
	chunks := splitReplyText(text, plan.TextLimit)
	p.Text = ""
	if plan.PrimaryText && len(chunks) > 0 {
		p.Text = chunks[0]
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	ops := []Op{{
		DeliveryKey: deliveryKey, Index: 0, Kind: OpSendGroupReply,
		ChannelID: channelID, AccountKey: accountKey, Address: rawAddr, Payload: payload,
	}}
	start := 0
	if plan.PrimaryText {
		start = 1
	}
	siblings, err := siblingOps("", deliveryKey, channelID, accountKey, rawAddr, len(ops), chunks[start:], images, files, plan.Attachments)
	if err != nil {
		return nil, err
	}
	ops = append(ops, siblings...)
	// The group's own row owns these ops' lifecycle: deleting the group drops
	// its pending sends and their attachment bodies, while deleting a channel
	// or dispatch row keeps the delivery ledger intact.
	for i := range ops {
		ops[i].GroupID = groupID
	}
	return ops, nil
}

// siblingOps emits one send_text op per chunk and, when attachments is set,
// one send_attachment op per media item — each chained on its predecessor so
// a retried op never lets a later one overtake it.
func siblingOps(runID, deliveryKey, channelID, accountKey string, rawAddr json.RawMessage, index int, chunks []string, images []pkgchannel.ImageEvent, files []pkgchannel.FileEvent, attachments bool) ([]Op, error) {
	ops := make([]Op, 0, len(chunks)+len(images)+len(files))
	for _, chunk := range chunks {
		p, err := json.Marshal(TextPayload{V: PayloadVersion, Text: chunk})
		if err != nil {
			return nil, err
		}
		ops = append(ops, Op{
			RunID: runID, DeliveryKey: deliveryKey, Index: index + len(ops), Kind: OpSendText,
			ChannelID: channelID, AccountKey: accountKey, Address: rawAddr, Payload: p,
			DependsOn: []int{index + len(ops) - 1},
		})
	}
	if attachments {
		for _, img := range images {
			op, err := attachmentOp(runID, deliveryKey, channelID, accountKey, rawAddr, index+len(ops),
				pkgchannel.AttachmentOpPayload{V: PayloadVersion, Kind: pkgchannel.AttachmentImage, Data: img.Data, MimeType: img.MimeType})
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
		for _, f := range files {
			if f.Data == nil {
				return nil, fmt.Errorf("attachment %q was never prepared — no durable bytes to send", f.Name)
			}
			op, err := attachmentOp(runID, deliveryKey, channelID, accountKey, rawAddr, index+len(ops),
				pkgchannel.AttachmentOpPayload{V: PayloadVersion, Kind: pkgchannel.AttachmentFile, Name: f.Name, MimeType: f.MimeType})
			if err != nil {
				return nil, err
			}
			// The frozen bytes travel on the op, not in the payload: Append
			// writes them to channel_outbox_attachment in the same
			// transaction and the send boundary reads them back by op id.
			op.Attachment = f.Data
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// foldMediaMarkers appends an explicit textual marker per media item so a
// platform without a binary upload path still tells the user a file existed.
func foldMediaMarkers(text string, images []pkgchannel.ImageEvent, files []pkgchannel.FileEvent) string {
	var sb strings.Builder
	sb.WriteString(text)
	for _, img := range images {
		fmt.Fprintf(&sb, "\n\n[Image: %s]", img.MimeType)
	}
	for _, f := range files {
		name := f.Name
		if name == "" {
			name = f.Path
		}
		fmt.Fprintf(&sb, "\n\n[File: %s]", name)
	}
	return sb.String()
}

func splitReplyText(text string, maxLen int) []string {
	if maxLen > 0 {
		return pkgchannel.SplitMessage(text, maxLen)
	}
	return []string{text}
}

func attachmentOp(runID, deliveryKey, channelID, accountKey string, rawAddr json.RawMessage, index int, p pkgchannel.AttachmentOpPayload) (Op, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return Op{}, err
	}
	return Op{
		RunID: runID, DeliveryKey: deliveryKey, Index: index, Kind: OpSendAttachment,
		ChannelID: channelID, AccountKey: accountKey, Address: rawAddr, Payload: payload,
		DependsOn: []int{index - 1},
	}, nil
}

// GroupReplyPayload is the frozen body of a send_group_reply op; the shared
// definition lives in pkg/channel so adapters decode the same shape.
type GroupReplyPayload = pkgchannel.GroupReplyOpPayload
