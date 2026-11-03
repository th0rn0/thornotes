package wikilinks

import "testing"

func TestExtractLinks_Inline(t *testing.T) {
	refs := ExtractLinks("See [[Overview]] and [[Other]].")
	if len(refs) != 2 || refs[0].Target != "Overview" || refs[1].Target != "Other" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_AnchorAndPipe(t *testing.T) {
	refs := ExtractLinks("[[Overview#Trust boundaries]] and [[Overview|the map]]")
	if len(refs) != 2 {
		t.Fatalf("len = %d", len(refs))
	}
	if refs[0].Target != "Overview" || refs[0].Anchor != "Trust boundaries" || refs[0].LinkText != "" {
		t.Fatalf("anchor ref = %+v", refs[0])
	}
	if refs[1].Target != "Overview" || refs[1].LinkText != "the map" || refs[1].Anchor != "" {
		t.Fatalf("pipe ref = %+v", refs[1])
	}
}

func TestExtractLinks_CodeFence(t *testing.T) {
	body := "before\n```\n[[not-a-link]]\n```\nafter [[real]]"
	refs := ExtractLinks(body)
	if len(refs) != 1 || refs[0].Target != "real" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_FencedWithLanguage(t *testing.T) {
	body := "```go\nfunc f() { /* [[not-a-link]] */ }\n```\n[[real]]"
	refs := ExtractLinks(body)
	if len(refs) != 1 || refs[0].Target != "real" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_InlineCode(t *testing.T) {
	refs := ExtractLinks("`[[not-a-link]]` and [[real]]")
	if len(refs) != 1 || refs[0].Target != "real" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_Table(t *testing.T) {
	body := "| Name | Link |\n|------|------|\n| Foo  | [[Bar]] |\n| Baz  | [[Qux]] |"
	refs := ExtractLinks(body)
	if len(refs) != 2 || refs[0].Target != "Bar" || refs[1].Target != "Qux" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_TildeFence(t *testing.T) {
	body := "~~~\n[[not-a-link]]\n~~~\n[[real]]"
	refs := ExtractLinks(body)
	if len(refs) != 1 || refs[0].Target != "real" {
		t.Fatalf("got %+v", refs)
	}
}

func TestExtractLinks_NoLinks(t *testing.T) {
	if refs := ExtractLinks("plain text with no links"); len(refs) != 0 {
		t.Fatalf("got %+v", refs)
	}
}
