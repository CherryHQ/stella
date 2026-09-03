package fakeanthropic

import "testing"

func TestModelScriptsDoNotConsumeFIFO(t *testing.T) {
	f := New()
	f.Fail = func(message string) { t.Error(message) }
	defer f.Close()
	f.EnqueueText("fifo")
	f.EnqueueTextForModel("agent-a", "a")
	f.mu.Lock()
	a, okA := f.selectResponse("agent-a", goalTurn{})
	b, okB := f.selectResponse("agent-b", goalTurn{})
	f.mu.Unlock()
	if !okA || !okB || a.text != "a" || b.text != "fifo" {
		t.Fatalf("model/FIFO responses = %q/%q", a.text, b.text)
	}
}
