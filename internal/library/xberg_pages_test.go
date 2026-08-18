package library

import "testing"

func TestEnforcePresentationPageBoundariesSplitsAndRemovesMarkers(t *testing.T) {
	t.Parallel()
	first, last := uint32(1), uint32(2)
	chunks := []ParsedChunk{{
		Content: "<!-- STELLA_LIBRARY_SLIDE 1 -->## Slide one\n\n<!-- STELLA_LIBRARY_SLIDE 2 -->## Slide two",
		Locator: ChunkLocator{FirstPage: &first, LastPage: &last, HeadingPath: []string{"Slide one"}, ByteStart: 10, ByteEnd: 104},
	}}
	got, err := enforcePresentationPageBoundaries(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "## Slide one" || got[1].Content != "## Slide two" {
		t.Fatalf("chunks = %+v", got)
	}
	if *got[0].Locator.FirstPage != 1 || *got[0].Locator.LastPage != 1 ||
		*got[1].Locator.FirstPage != 2 || *got[1].Locator.LastPage != 2 {
		t.Fatalf("locators = %+v, %+v", got[0].Locator, got[1].Locator)
	}
	if len(got[0].Locator.HeadingPath) != 0 || len(got[1].Locator.HeadingPath) != 0 {
		t.Fatalf("cross-slide heading paths = %v, %v", got[0].Locator.HeadingPath, got[1].Locator.HeadingPath)
	}
}

func TestEnforcePresentationPageBoundariesAssignsPrefixBeforeLaterMarker(t *testing.T) {
	t.Parallel()
	first, last := uint32(1), uint32(2)
	got, err := enforcePresentationPageBoundaries([]ParsedChunk{{
		Content: "tail of slide one\n<!-- STELLA_LIBRARY_SLIDE 2 -->slide two",
		Locator: ChunkLocator{FirstPage: &first, LastPage: &last, ByteEnd: 59},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || *got[0].Locator.FirstPage != 1 || *got[1].Locator.FirstPage != 2 {
		t.Fatalf("chunks = %+v", got)
	}
}

func TestEnforcePresentationPageBoundariesLeavesSingleSlideWithoutMarker(t *testing.T) {
	t.Parallel()
	page := uint32(3)
	want := []ParsedChunk{{Content: "body", Locator: ChunkLocator{FirstPage: &page, LastPage: &page, ByteEnd: 4}}}
	got, err := enforcePresentationPageBoundaries(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "body" {
		t.Fatalf("chunks = %+v", got)
	}
}

func TestEnforcePresentationPageBoundariesRejectsUnlocatableCrossSlideChunk(t *testing.T) {
	t.Parallel()
	first, last := uint32(1), uint32(2)
	_, err := enforcePresentationPageBoundaries([]ParsedChunk{{
		Content: "both slides", Locator: ChunkLocator{FirstPage: &first, LastPage: &last, ByteEnd: 11},
	}})
	if err == nil {
		t.Fatal("expected missing marker error")
	}
}
