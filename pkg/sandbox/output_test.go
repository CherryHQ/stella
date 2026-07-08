package sandbox

import "testing"

func TestCappedOutputBufferBoundary(t *testing.T) {
	buf := NewCappedOutputBuffer(5)
	if n, err := buf.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("Write = %d, %v; want 5, nil", n, err)
	}
	if got := buf.String(); got != "hello" {
		t.Fatalf("String = %q, want hello", got)
	}
	if buf.Truncated() {
		t.Fatal("exact-limit write should not be marked truncated")
	}

	if n, err := buf.Write([]byte("!")); err != nil || n != 1 {
		t.Fatalf("Write overflow = %d, %v; want 1, nil", n, err)
	}
	want := "hello" + execOutputTruncatedMarker
	if got := buf.String(); got != want {
		t.Fatalf("String after overflow = %q, want %q", got, want)
	}
	if !buf.Truncated() {
		t.Fatal("overflow write should be marked truncated")
	}
}

func TestCappedOutputBufferTruncatesAcrossWrites(t *testing.T) {
	buf := NewCappedOutputBuffer(5)
	_, _ = buf.Write([]byte("he"))
	_, _ = buf.Write([]byte("llo!"))

	want := "hello" + execOutputTruncatedMarker
	if got := buf.String(); got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
}
