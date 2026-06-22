package pgnull

import "testing"

func TestText(t *testing.T) {
	if got := Text(""); got.Valid {
		t.Errorf(`Text("") = %+v, want NULL`, got)
	}
	got := Text("x")
	if !got.Valid || got.String != "x" {
		t.Errorf(`Text("x") = %+v, want {String:"x", Valid:true}`, got)
	}
}

func TestTextTrim(t *testing.T) {
	if got := TextTrim("   \t\n"); got.Valid {
		t.Errorf("TextTrim(whitespace) = %+v, want NULL", got)
	}
	got := TextTrim("  x  ")
	if !got.Valid || got.String != "x" {
		t.Errorf(`TextTrim("  x  ") = %+v, want {String:"x", Valid:true}`, got)
	}
}
