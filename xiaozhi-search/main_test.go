package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchRejectsInvalidBoundsBeforeNetwork(t *testing.T) {
	previous := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached network")
		return nil, nil
	})}
	defer func() { httpClient = previous }()
	for _, in := range []input{{Query: "  "}, {Query: strings.Repeat("ệ", 501)}, {Query: "valid", MaxResults: -1}, {Query: "valid", MaxResults: 6}} {
		if _, err := search(context.Background(), in); err == nil {
			t.Errorf("accepted invalid bounds: %+v", in)
		}
	}
}

func TestClippedKeepsVietnameseUTF8AtByteBoundary(t *testing.T) {
	text := "Hôm nay tiếng Việt: " + strings.Repeat("ệ", 100)
	for _, limit := range []int{0, 1, 20, 21, 22, 240, len(text)} {
		got := clipped(text, limit)
		if len(got) > limit || !utf8.ValidString(got) || !strings.HasPrefix(text, got) {
			t.Fatalf("corrupted source excerpt at %d bytes: %q", limit, got)
		}
		if len(got) < len(text) && len(got)+utf8.RuneLen([]rune(text[len(got):])[0]) <= limit {
			t.Fatalf("discarded a complete character within %d bytes", limit)
		}
	}
}

func TestSearchRejectsExaQuotaNoticeEvenWithoutErrorFlag(t *testing.T) {
	response := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{
		Text: "You've hit Exa's free MCP rate limit. To continue using without limits, create your own Exa API key.\n\nFix: Create API key at https://dashboard.exa.ai/api-keys",
	}}}
	out, err := searchOutput(response)
	if err == nil || out.SourceText != "" {
		t.Fatal("quota notice was exposed as successful search results")
	}
}

func TestSearchPreservesVietnameseSourcesAndBoundsResponse(t *testing.T) {
	text := "Title: Chính phủ Việt Nam\nURL: https://chinhphu.vn/\nText: " + strings.Repeat("ệ", 5000)
	out, err := searchOutput(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.SourceText, "https://chinhphu.vn/") || !strings.Contains(out.SourceText, "Chính phủ Việt Nam") ||
		len(out.SourceText) > 12000 || !utf8.ValidString(out.SourceText) {
		t.Fatal("source URL or Vietnamese excerpt was lost, corrupted, or unbounded")
	}
}
