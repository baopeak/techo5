package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	appControlSocket   = "/run/techo5-app-control/control.sock"
	appControlTimeout  = 3 * time.Second
	appControlMaxBytes = 4096
)

type appControlRequest struct {
	Action string `json:"action"`
	App    string `json:"app,omitempty"`
	Query  string `json:"query,omitempty"`
}

type appControlResponse struct {
	OK        bool   `json:"ok"`
	State     string `json:"state"`
	App       string `json:"app,omitempty"`
	RequestID uint64 `json:"request_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

type appControlClient struct {
	// Internal fields permit isolated Unix-socket tests, not user configuration.
	socketPath string
	timeout    time.Duration
}

func validateAppRequest(in appControlRequest) (appControlRequest, error) {
	switch in.Action {
	case "open":
		if in.App != "browser" && in.App != "youtube" && in.App != "netflix" {
			return appControlRequest{}, errors.New("app must be browser, youtube or netflix")
		}
		if in.Query != "" {
			return appControlRequest{}, errors.New("open does not accept a query")
		}
	case "search":
		if in.App != "web" && in.App != "youtube" && in.App != "netflix" {
			return appControlRequest{}, errors.New("target must be web, youtube or netflix")
		}
		if !utf8.ValidString(in.Query) {
			return appControlRequest{}, errors.New("query must be valid UTF-8")
		}
		in.Query = strings.TrimSpace(in.Query)
		if n := utf8.RuneCountInString(in.Query); n < 2 || n > 500 {
			return appControlRequest{}, errors.New("query must contain 2 to 500 characters after trimming")
		}
	case "close", "status":
		if in.App != "" || in.Query != "" {
			return appControlRequest{}, errors.New("close and status do not accept app or query")
		}
	default:
		return appControlRequest{}, errors.New("unsupported app-control action")
	}
	return in, nil
}

func (c appControlClient) call(ctx context.Context, in appControlRequest) (appControlResponse, error) {
	in, err := validateAppRequest(in)
	if err != nil {
		return appControlResponse{}, err
	}
	payload, err := json.Marshal(in)
	if err != nil || len(payload)+1 > appControlMaxBytes {
		return appControlResponse{}, errors.New("app-control request exceeds the 4096-byte limit")
	}
	timeout := c.timeout
	if timeout <= 0 || timeout > appControlTimeout {
		timeout = appControlTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return appControlResponse{}, errors.New("Echo app control unavailable; ensure echod and its app-control socket are running")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return appControlResponse{}, errors.New("Echo app-control connection could not set its deadline")
	}
	if _, err := io.Copy(conn, bytes.NewReader(append(payload, '\n'))); err != nil {
		return appControlResponse{}, errors.New("Echo app-control request failed; check app_status before retrying")
	}
	line, err := bufio.NewReaderSize(conn, appControlMaxBytes+1).ReadSlice('\n')
	if err != nil || len(line) > appControlMaxBytes {
		return appControlResponse{}, errors.New("Echo app control returned no complete bounded response; check app_status before retrying")
	}
	if !utf8.Valid(line) {
		return appControlResponse{}, errors.New("Echo app control returned an invalid response")
	}
	var out appControlResponse
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return appControlResponse{}, errors.New("Echo app control returned an invalid response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return appControlResponse{}, errors.New("Echo app control returned trailing response data")
	}
	if out.App != "" && out.App != "browser" && out.App != "youtube" && out.App != "netflix" {
		return appControlResponse{}, errors.New("Echo app control returned an invalid app")
	}
	switch out.State {
	case "queued", "starting", "open", "closed", "error":
	default:
		return appControlResponse{}, errors.New("Echo app control returned an invalid state")
	}
	if !out.OK || out.State == "error" || out.Error != "" {
		// Daemon errors are bounded, safe actionable messages, never queries or URLs.
		if out.Error == "" {
			return appControlResponse{}, errors.New("Echo app control rejected the request; check app_status and the local browser launcher")
		}
		return appControlResponse{}, errors.New("Echo app control: " + clipped(out.Error, 500))
	}
	return out, nil
}

type openAppInput struct {
	App string `json:"app"`
}

type searchOnScreenInput struct {
	Target string `json:"target"`
	Query  string `json:"query"`
}

type emptyAppInput struct{}

func addAppTools(server *mcp.Server, status *statusFile, client appControlClient) {
	no, yes := false, true
	mutating := func() *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &yes, IdempotentHint: false, OpenWorldHint: &yes}
	}
	call := func(ctx context.Context, name string, in appControlRequest) (*mcp.CallToolResult, appControlResponse, error) {
		status.update(func(s *state) {
			s.Calls++
			switch name {
			case "open_app":
				s.OpenAppCalls++
			case "search_on_screen":
				s.SearchOnScreenCalls++
			case "close_app":
				s.CloseAppCalls++
			case "app_status":
				s.AppStatusCalls++
			}
		})
		out, err := client.call(ctx, in)
		status.update(func(s *state) {
			if err != nil {
				s.AppErrors++
			}
			if err == nil && out.State == "queued" {
				s.AppQueuedResponses++
			}
		})
		return nil, out, err
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "open_app",
		Description: "Mở ứng dụng trên màn hình Echo: browser, youtube hoặc netflix. Queued/starting is not open; use app_status to confirm readiness. Does not guarantee sign-in or playback.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"app":{"type":"string","enum":["browser","youtube","netflix"]}},"required":["app"],"additionalProperties":false}`),
		Annotations: mutating(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in openAppInput) (*mcp.CallToolResult, appControlResponse, error) {
		return call(ctx, "open_app", appControlRequest{Action: "open", App: in.App})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_on_screen",
		Description: "Tìm kiếm trên màn hình Echo trong web, youtube hoặc netflix. Navigates the display, not web_search evidence. Queued/starting is not visible; use app_status. Query is 2–500 characters after trimming.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","enum":["web","youtube","netflix"]},"query":{"type":"string","minLength":2,"maxLength":500}},"required":["target","query"],"additionalProperties":false}`),
		Annotations: mutating(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchOnScreenInput) (*mcp.CallToolResult, appControlResponse, error) {
		return call(ctx, "search_on_screen", appControlRequest{Action: "search", App: in.Target, Query: in.Query})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "close_app",
		Description: "Đóng ứng dụng màn hình Echo và hủy yêu cầu đang chờ. Closure may be asynchronous; only state closed confirms completion.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: mutating(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyAppInput) (*mcp.CallToolResult, appControlResponse, error) {
		return call(ctx, "close_app", appControlRequest{Action: "close"})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_status",
		Description: "Đọc trạng thái ứng dụng màn hình Echo: queued, starting, open, closed hoặc error. Only open confirms browser readiness, not account login or media playback.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyAppInput) (*mcp.CallToolResult, appControlResponse, error) {
		return call(ctx, "app_status", appControlRequest{Action: "status"})
	})
}
