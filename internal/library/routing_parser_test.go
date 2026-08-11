package library

import (
	"context"
	"errors"
	"testing"
)

type routingTestParser struct{ profile string }

func (p routingTestParser) Profile(string) (string, error) { return p.profile, nil }
func (routingTestParser) Parse(context.Context, string, string) ([]ParsedChunk, error) {
	return []ParsedChunk{{Content: "routed"}}, nil
}

func TestRoutingParserCopiesRoutesAndDelegatesProfile(t *testing.T) {
	routes := map[string]Parser{MediaTypePDF: routingTestParser{profile: "pdf:v1"}}
	parser, err := NewRoutingParser(routes)
	if err != nil {
		t.Fatal(err)
	}
	delete(routes, MediaTypePDF)
	profile, err := parser.Profile(MediaTypePDF)
	if err != nil || profile != "pdf:v1" {
		t.Fatalf("Profile = %q, %v", profile, err)
	}
	if _, err := parser.Profile("image/png"); !errors.Is(err, ErrUnsupportedFileType) {
		t.Fatalf("Profile error = %v, want ErrUnsupportedFileType", err)
	}
	chunks, err := parser.Parse(t.Context(), "source.pdf", MediaTypePDF)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "routed" {
		t.Fatalf("Parse = %+v, %v", chunks, err)
	}
}
