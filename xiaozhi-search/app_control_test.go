package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func appToolSession(t *testing.T, client appControlClient) (*mcp.ClientSession, *statusFile) {
	t.Helper()
	status := &statusFile{}
	server := mcp.NewServer(&mcp.Implementation{Name: "app-test", Version: version}, nil)
	addAppTools(server, status, client)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "consumer", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, status
}

func callAppTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func requireAppFailure(t *testing.T, result *mcp.CallToolResult, err error) {
	t.Helper()
	if err == nil && (result == nil || !result.IsError) {
		t.Fatal("consumer received success for rejected app operation")
	}
}

func TestAppToolsRejectInvalidConsumerArguments(t *testing.T) {
	session, status := appToolSession(t, appControlClient{socketPath: filepath.Join(t.TempDir(), "absent.sock")})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"open_app", map[string]any{"app": "shell"}},
		{"open_app", map[string]any{"app": "browser", "url": "https://example.com"}},
		{"open_app", map[string]any{}},
		{"search_on_screen", map[string]any{"target": "browser", "query": "hello"}},
		{"search_on_screen", map[string]any{"target": "web", "query": "  "}},
		{"search_on_screen", map[string]any{"target": "web", "query": strings.Repeat("ệ", 501)}},
		{"play_youtube", map[string]any{"query": " "}},
		{"play_youtube", map[string]any{"query": "chuyện đêm muộn", "target": "netflix"}},
		{"play_youtube", map[string]any{}},
		{"close_app", map[string]any{"app": "browser"}},
		{"app_status", map[string]any{"command": "id"}},
	} {
		t.Run(tc.name+"/"+string(mustJSON(t, tc.args)), func(t *testing.T) {
			result, err := callAppTool(t, session, tc.name, tc.args)
			requireAppFailure(t, result, err)
		})
	}
	if status.state.AppQueuedResponses != 0 || status.state.Successes != 0 {
		t.Fatal("invalid app calls counted as accepted or successful searches")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAppToolsUnavailableIsVisibleFailure(t *testing.T) {
	session, status := appToolSession(t, appControlClient{socketPath: filepath.Join(t.TempDir(), "missing.sock")})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"open_app", map[string]any{"app": "youtube"}},
		{"search_on_screen", map[string]any{"target": "netflix", "query": "tiếng Việt"}},
		{"play_youtube", map[string]any{"query": "chuyện đêm muộn tập 14"}},
		{"close_app", map[string]any{}},
		{"app_status", map[string]any{}},
	} {
		result, err := callAppTool(t, session, tc.name, tc.args)
		requireAppFailure(t, result, err)
		if err == nil {
			text := string(mustJSON(t, result))
			if !strings.Contains(text, "unavailable") {
				t.Fatal("missing actionable bridge error")
			}
		}
	}
	if status.state.AppErrors != 5 || status.state.OpenAppCalls != 1 || status.state.SearchOnScreenCalls != 1 || status.state.PlayYouTubeCalls != 1 || status.state.CloseAppCalls != 1 || status.state.AppStatusCalls != 1 || status.state.Successes != 0 {
		t.Fatalf("app and search counters conflated: %+v", status.state)
	}
}

// A real Unix listener exercises framing, deadlines and consumer-visible failures.
// Responses model protocol failures, not a fake implementation of app opening.
func serveAppResponse(t *testing.T, respond func(net.Conn)) string {
	t.Helper()
	// Unix socket path limits apply on Windows too; t.TempDir embeds the full
	// subtest name and can exceed sockaddr_un before the protocol is exercised.
	dir, err := os.MkdirTemp("", "t5-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		respond(conn)
	}()
	return path
}

func TestAppToolsBoundAndRejectInvalidBridgeResponses(t *testing.T) {
	for _, response := range []string{
		strings.Repeat("x", appControlMaxBytes) + "\n",
		`{"ok":true,"state":"open","secret":"unexpected"}` + "\n",
		`{"ok":true,"state":"open"} {}` + "\n",
		`{"ok":true,"state":"invented"}` + "\n",
		`{"ok":true,"state":"open","app":"shell"}` + "\n",
		`{"ok":false,"state":"error","error":"browser launcher unavailable"}` + "\n",
		"{\"ok\":true,\"state\":\"open\",\"app\":\"\xff\"}\n",
		`{"ok":true,"state":"open"}`, // Missing newline is not a complete response.
		"",
	} {
		t.Run(string(mustJSON(t, response)), func(t *testing.T) {
			path := serveAppResponse(t, func(conn net.Conn) {
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = io.WriteString(conn, response)
			})
			session, _ := appToolSession(t, appControlClient{socketPath: path})
			result, err := callAppTool(t, session, "app_status", map[string]any{})
			requireAppFailure(t, result, err)
		})
	}
}

func TestAppToolsTimeoutDoesNotClaimSuccess(t *testing.T) {
	path := serveAppResponse(t, func(conn net.Conn) {
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, _ = io.Copy(io.Discard, conn) // Wait until the bounded client closes.
	})
	session, status := appToolSession(t, appControlClient{socketPath: path, timeout: 40 * time.Millisecond})
	start := time.Now()
	result, err := callAppTool(t, session, "open_app", map[string]any{"app": "browser"})
	requireAppFailure(t, result, err)
	if time.Since(start) > time.Second || status.state.AppQueuedResponses != 0 {
		t.Fatal("stalled bridge blocked the tool or counted success")
	}
}

func TestQueuedScreenSearchPreservesVietnameseWithoutClaimingOpen(t *testing.T) {
	requests := make(chan appControlRequest, 1)
	path := serveAppResponse(t, func(conn net.Conn) {
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		var request appControlRequest
		_ = json.Unmarshal(line, &request)
		requests <- request
		_, _ = io.WriteString(conn, "{\"ok\":true,\"state\":\"queued\",\"app\":\"youtube\",\"request_id\":7}\n")
	})
	session, status := appToolSession(t, appControlClient{socketPath: path})
	result, err := callAppTool(t, session, "search_on_screen", map[string]any{"target": "youtube", "query": "  nhạc tiếng Việt & cà phê  "})
	if err != nil || result.IsError {
		t.Fatalf("queued request failed: %v %+v", err, result)
	}
	text := string(mustJSON(t, result))
	if !strings.Contains(text, "queued") || strings.Contains(text, `"state":"open"`) || strings.Contains(text, "nhạc") {
		t.Fatal("queued result claimed opened or exposed the query")
	}
	request := <-requests
	if request.Action != "search" || request.App != "youtube" || request.Query != "nhạc tiếng Việt & cà phê" {
		t.Fatalf("Unicode query or fixed action changed: %+v", request)
	}
	if status.state.AppQueuedResponses != 1 || status.state.Successes != 0 || status.state.SearchOnScreenCalls != 1 {
		t.Fatal("queued request falsely counted as successful search")
	}
}

// Playing a named video sends the fixed play action for YouTube with the title as heard; the
// model cannot pick another provider.
func TestPlayYouTubeSendsTheTitleToPlay(t *testing.T) {
	requests := make(chan appControlRequest, 1)
	path := serveAppResponse(t, func(conn net.Conn) {
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		var request appControlRequest
		_ = json.Unmarshal(line, &request)
		requests <- request
		_, _ = io.WriteString(conn, "{\"ok\":true,\"state\":\"queued\",\"app\":\"youtube\",\"request_id\":8}\n")
	})
	session, status := appToolSession(t, appControlClient{socketPath: path})
	result, err := callAppTool(t, session, "play_youtube", map[string]any{"query": " chuyện đêm muộn tập 14 "})
	if err != nil || result.IsError {
		t.Fatalf("queued play failed: %v %+v", err, result)
	}
	request := <-requests
	if request.Action != "play" || request.App != "youtube" || request.Query != "chuyện đêm muộn tập 14" {
		t.Fatalf("play request changed: %+v", request)
	}
	if status.state.PlayYouTubeCalls != 1 || status.state.AppQueuedResponses != 1 {
		t.Fatalf("play not counted: %+v", status.state)
	}
}

func TestAppToolsExposeStrictSchemasAndMutationHints(t *testing.T) {
	session, _ := appToolSession(t, appControlClient{})
	catalog, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tools) != 5 {
		t.Fatal("missing app tools")
	}
	for _, tool := range catalog.Tools {
		var schema map[string]any
		if err := json.Unmarshal(mustJSON(t, tool.InputSchema), &schema); err != nil {
			t.Fatal(err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s allows unknown properties", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (tool.Name == "app_status") {
			t.Fatalf("%s has untruthful mutation hints", tool.Name)
		}
	}
}

func TestAppRequestValidationRejectsIrrelevantAndInvalidUTF8Input(t *testing.T) {
	client := appControlClient{socketPath: filepath.Join(t.TempDir(), "absent.sock")}
	for _, in := range []appControlRequest{
		{Action: "close", App: "browser"},
		{Action: "status", Query: "hidden"},
		{Action: "open", App: "browser", Query: "hidden"},
		{Action: "search", App: "web", Query: "a\xff"},
		{Action: "play", App: "netflix", Query: "chuyện đêm muộn"},
		{Action: "play", App: "youtube", Query: "x"},
	} {
		_, err := client.call(context.Background(), in)
		if err == nil || strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("invalid request reached bridge: %v", err)
		}
	}
}
