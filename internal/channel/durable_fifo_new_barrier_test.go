package channel

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// Distinct /new messages admitted before the first one reaches the consumer
// carry the same admission-time expected session. The FIFO executes the first
// rotation and treats the second as a stale compare, preserving the existing
// destructive-command contract of one reset for the concurrent batch.
func TestDurableIngressDistinctNewCommandsAdmittedBeforeExecutionRotateOnce(t *testing.T) {
	c, db, _, _ := newDurableRuntimeAcceptanceCoordinator(t)
	d := NewDurableIngress(db, "new-barrier-contract")
	d.BindCoordinator(c)
	ctx := context.Background()
	newMessage := func(id string) pkgchannel.IncomingMessage {
		return pkgchannel.IncomingMessage{
			MessageID: id, Platform: "telegram", ChannelID: "telegram", SenderID: "runtime-sender",
			ChatID: "runtime-chat", Content: []ai.ContentBlock{ai.TextContent{Text: "/new"}},
		}
	}
	firstMessage, secondMessage := newMessage("distinct-new-1"), newMessage("distinct-new-2")
	before, err := c.resolve(ctx, firstMessage)
	if err != nil {
		t.Fatalf("resolve before /new: %v", err)
	}
	beforeSession, err := before.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("resolve session before /new: %v", err)
	}

	_, handled, first, err := d.Admit(ctx, firstMessage, newSessionCommand, "")
	if err != nil || handled || first == nil {
		t.Fatalf("first /new Admit = handled:%v stream:%v err:%v", handled, first != nil, err)
	}
	_, handled, second, err := d.Admit(ctx, secondMessage, newSessionCommand, "")
	if err != nil || handled || second == nil {
		t.Fatalf("second /new Admit = handled:%v stream:%v err:%v", handled, second != nil, err)
	}

	processNew := func(stream *pkgchannel.ChatStream, want string) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- d.processOne(ctx) }()
		select {
		case event := <-stream.Events:
			if event.Text != want {
				t.Fatalf("/new reply = %q, want %q", event.Text, want)
			}
		case err := <-done:
			t.Fatalf("/new consumer exited before reply: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("/new consumer did not produce a reply")
		}
		if err := stream.Completion.Ack(ctx, pkgchannel.EgressDelivered); err != nil {
			t.Fatalf("/new Ack: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("/new processOne: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("/new consumer did not settle")
		}
	}

	processNew(first, pkgchannel.NewSessionStartedMessage)
	afterFirst, err := c.resolve(ctx, firstMessage)
	if err != nil {
		t.Fatalf("resolve after first /new: %v", err)
	}
	firstSuccessor, err := afterFirst.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("resolve first successor: %v", err)
	}
	if firstSuccessor.ID == beforeSession.ID {
		t.Fatalf("first /new kept session %s", beforeSession.ID)
	}

	processNew(second, pkgchannel.SessionAlreadyResetMessage)
	afterSecond, err := c.resolve(ctx, secondMessage)
	if err != nil {
		t.Fatalf("resolve after second /new: %v", err)
	}
	secondCurrent, err := afterSecond.ResolveSession(ctx)
	if err != nil {
		t.Fatalf("resolve current after second /new: %v", err)
	}
	if secondCurrent.ID != firstSuccessor.ID {
		t.Fatalf("distinct second /new rotated successor %s to %s", firstSuccessor.ID, secondCurrent.ID)
	}
}
