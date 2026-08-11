package tools

import (
	"errors"
	"testing"
)

func TestInvalidInputMarksAndPreservesCause(t *testing.T) {
	cause := errors.New("bad arguments")
	err := InvalidInput(cause)
	if !IsInvalidInput(err) {
		t.Fatal("marked error was not recognized as invalid input")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("marked error does not preserve cause: %v", err)
	}
	if !errors.Is(InvalidInput(err), err) {
		t.Fatal("marking invalid input twice changed the error")
	}
	if InvalidInput(nil) != nil {
		t.Fatal("marking nil returned a non-nil error")
	}
}
