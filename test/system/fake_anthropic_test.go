//go:build system

package system

import (
	"context"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/test/fakeanthropic"
)

type (
	fakeRequest = fakeanthropic.Request
	turnGate    struct {
		*fakeanthropic.TurnGate
		release chan struct{}
	}
)

type fakeAnthropic struct{ *fakeanthropic.Fake }

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{Fake: fakeanthropic.New()}
	f.Fail = func(message string) { t.Error(message) }
	t.Cleanup(func() { f.Close() })
	return f
}

func (f *fakeAnthropic) baseURL() string                        { return f.BaseURL() }
func (f *fakeAnthropic) enqueueText(text string)                { f.EnqueueText(text) }
func (f *fakeAnthropic) enqueueTextForModel(model, text string) { f.EnqueueTextForModel(model, text) }
func (f *fakeAnthropic) setTrailingTextForModel(model, text string) {
	f.SetTrailingTextForModel(model, text)
}
func (f *fakeAnthropic) discardModelScripts()                   { f.DiscardModelScripts() }
func (f *fakeAnthropic) requestCount() int                      { return f.RequestCount() }
func (f *fakeAnthropic) enqueueTool(id, name, args string)      { f.EnqueueTool(id, name, args) }
func (f *fakeAnthropic) enqueueGoalControl(action, args string) { f.EnqueueGoalControl(action, args) }
func (f *fakeAnthropic) enqueueError(status int, errType, message string) {
	f.EnqueueError(status, errType, message)
}

func (f *fakeAnthropic) enqueueGatedText(first, second string) *turnGate {
	gate := f.EnqueueGatedText(first, second)
	return &turnGate{TurnGate: gate, release: gate.ReleaseChan}
}
func (f *fakeAnthropic) requests() []fakeRequest { return f.Requests() }
func (f *fakeAnthropic) waitForRequests(ctx context.Context, want int) []fakeRequest {
	return f.WaitForRequests(ctx, want)
}

// Keep this compile-time assertion close to the adapter. The shared package
// owns HTTP parsing and SSE framing; system tests only retain their readable API.
var _ http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
