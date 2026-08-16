package mdutil_test

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/goldmark/mdutil"
)

func TestFindDetails(t *testing.T) {
	src := "intro\n\n<details open>\n<summary>Title</summary>\n\nbody\n\n</details>\n\ntail\n"
	found := mdutil.FindDetails(src)
	if len(found) != 1 {
		t.Fatalf("sections: got %d, want 1", len(found))
	}
	d := found[0]
	if !d.Open {
		t.Error("open attribute not detected")
	}
	if d.Summary != "Title" {
		t.Errorf("summary: got %q, want Title", d.Summary)
	}
	if d.Body != "body" {
		t.Errorf("body: got %q, want body", d.Body)
	}
	if src[d.Start:d.End] != "<details open>\n<summary>Title</summary>\n\nbody\n\n</details>" {
		t.Errorf("range: got %q", src[d.Start:d.End])
	}
}

func TestFindDetailsNoSummary(t *testing.T) {
	found := mdutil.FindDetails("<details>\ntext\n</details>")
	if len(found) != 1 || found[0].Summary != "" || found[0].Open {
		t.Fatalf("got %+v", found)
	}
}

func TestFindDetailsSkipsCode(t *testing.T) {
	if found := mdutil.FindDetails("```html\n<details>\nx\n</details>\n```"); len(found) != 0 {
		t.Errorf("fenced section matched: %+v", found)
	}
	if found := mdutil.FindDetails("use `<details>x</details>` here"); len(found) != 0 {
		t.Errorf("code span matched: %+v", found)
	}
}

func TestExpandAutoLinks(t *testing.T) {
	tests := []struct{ in, want string }{
		{"see <https://example.com> now", "see [https://example.com](https://example.com) now"},
		{"<a@b.com>", "[a@b.com](mailto:a@b.com)"},
		{"bare https://example.com", "bare https://example.com"},
		{"<font color=red>hi</font>", "<font color=red>hi</font>"},
		{"`<https://example.com>`", "`<https://example.com>`"},
	}
	for _, tt := range tests {
		if got := mdutil.ExpandAutoLinks(tt.in); got != tt.want {
			t.Errorf("ExpandAutoLinks(%q):\ngot  %q\nwant %q", tt.in, got, tt.want)
		}
	}
}
