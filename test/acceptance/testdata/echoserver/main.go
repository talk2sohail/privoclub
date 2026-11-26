//go:build acceptance

// Command echoserver is a stdio MCP server used only by the acceptance suite.
//
// Modes:
//
//	default     tool "echo", prompt "greet", resource "echo://note"
//	templates   adds tool "shout", prompt "farewell", resource "echo://secret", and templates
//	            "echo://items/{id}", "echo://files/{+path}", "echo://search{?q,limit}"
//	tools-only  tool "ping" only; writes any prompts/* or resources/* request to $FLAG
//	flaky       exits with status 1 until it has been started twice (counts in $MARKER), then default
//	annotated   tools "read_note" (readOnlyHint) and "delete_note" (destructiveHint)
//	outcomes    tools "fine" (succeeds), "fail" (a tool error: isError) and "boom" (a JSON-RPC error)
//	legacy      as default, but speaks only protocol versions before 2026-07-28, like most servers today
//	raw-notify  a hand-written server (no SDK) that sends one notification, -method M with -params absent|null,
//	            right after the handshake: what the SDK would never let a server send
//	hostile     as default, but with -null tools|prompts|resources one list carries a JSON null entry,
//	            with -null read a resources/read reply has a null entry in "contents",
//	            with -null messages a prompts/get reply has "messages": null, with -null contents a
//	            resources/read reply has "contents": null, and with -null poison every tools/call,
//	            prompts/get and resources/read result carries a requestState and reserved _meta keys
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Text string `json:"text" jsonschema:"text to repeat"`
}

func main() {
	mode := flag.String("mode", "default", "default, tools-only, flaky, annotated, legacy or hostile")
	method := flag.String("method", "", "with -mode raw-notify: the notification method to send")
	params := flag.String("params", "absent", "with -mode raw-notify: absent or null")
	null := flag.String("null", "", "with -mode hostile: where to put a JSON null: tools, prompts, resources, read, messages or contents; poison: no null, but a requestState and reserved _meta keys on every result")
	flag.Parse()
	if *mode == "raw-notify" {
		rawNotify(*method, *params)
		return
	}

	if *mode == "flaky" {
		marker := os.Getenv("MARKER")
		f, err := os.OpenFile(marker, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			log.Fatal(err)
		}
		_, _ = f.WriteString("x")
		_ = f.Close()
		b, _ := os.ReadFile(marker)
		if len(b) < 2 {
			os.Exit(1)
		}
	}

	var opts *mcp.ServerOptions
	if *mode == "legacy" {
		// Results from such a server carry no "resultType"; the gateway has
		// to add it for clients on 2026-07-28 (resulttype_test.go).
		opts = &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}}
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "0.0.1"}, opts)

	if *mode == "annotated" {
		yes := true
		mcp.AddTool(s, &mcp.Tool{Name: "read_note", Description: "Read a note.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "note"}}}, nil, nil
			})
		mcp.AddTool(s, &mcp.Tool{Name: "delete_note", Description: "Delete a note.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}}}, nil, nil
			})
	} else if *mode == "outcomes" {
		mcp.AddTool(s, &mcp.Tool{Name: "fine", Description: "Succeeds."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fine"}}}, nil, nil
			})
		mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Returns a tool error."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "failed on purpose"}}}, nil, nil
			})
		// A raw handler: an error it returns is a JSON-RPC error, not a tool result.
		s.AddTool(&mcp.Tool{Name: "boom", Description: "Returns a protocol error.", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, errors.New("boom")
			})
	} else if *mode == "tools-only" {
		s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if strings.HasPrefix(method, "prompts/") || strings.HasPrefix(method, "resources/") {
					if f, err := os.OpenFile(os.Getenv("FLAG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
						_, _ = f.WriteString(method + "\n")
						_ = f.Close()
					}
				}
				return next(ctx, method, req)
			}
		})
		mcp.AddTool(s, &mcp.Tool{Name: "ping", Description: "Answer pong."},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
			})
	} else if *mode == "templates" {
		mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Repeat the text you were given."},
			func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
			})
		mcp.AddTool(s, &mcp.Tool{Name: "shout", Description: "Repeat the text in capitals."},
			func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.ToUpper(in.Text)}}}, nil, nil
			})
		s.AddPrompt(&mcp.Prompt{
			Name:        "greet",
			Description: "Say hello",
			Arguments:   []*mcp.PromptArgument{{Name: "name", Description: "Who to greet", Required: true}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: "Hello " + req.Params.Arguments["name"]},
			}}}, nil
		})
		s.AddPrompt(&mcp.Prompt{
			Name:        "farewell",
			Description: "Say goodbye",
			Arguments:   []*mcp.PromptArgument{{Name: "name", Description: "Who to bid farewell", Required: true}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: "Goodbye " + req.Params.Arguments["name"]},
			}}}, nil
		})
		s.AddResource(&mcp.Resource{URI: "echo://note", Name: "note", MIMEType: "text/plain", Description: "A short note"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "echo://note", MIMEType: "text/plain", Text: "note-body"}}}, nil
			})
		s.AddResource(&mcp.Resource{URI: "echo://secret", Name: "secret", MIMEType: "text/plain", Description: "A secret note"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "echo://secret", MIMEType: "text/plain", Text: "secret-body"}}}, nil
			})
		tmplHandler := func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: "tpl:" + req.Params.URI}}}, nil
		}
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "echo://items/{id}", Name: "item", Description: "An item by id"}, tmplHandler)
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "echo://files/{+path}", Name: "file", Description: "A file by path"}, tmplHandler)
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "echo://search{?q,limit}", Name: "search", Description: "Search items"}, tmplHandler)
	} else {
		mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Repeat the text you were given."},
			func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
			})
		s.AddPrompt(&mcp.Prompt{
			Name:        "greet",
			Description: "Say hello",
			Arguments:   []*mcp.PromptArgument{{Name: "name", Description: "Who to greet", Required: true}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: "Hello " + req.Params.Arguments["name"]},
			}}}, nil
		})
		s.AddResource(&mcp.Resource{URI: "echo://note", Name: "note", MIMEType: "text/plain", Description: "A short note"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "echo://note", MIMEType: "text/plain", Text: "note-body"}}}, nil
			})
	}

	if *mode == "hostile" {
		s.AddReceivingMiddleware(hostile(*null))
	}

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// hostile makes one reply break the protocol: a JSON null in a list, or in the
// contents of a read. A conforming server never does, but an upstream is
// untrusted input, so the gateway has to survive it.
func hostile(where string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			switch r := res.(type) {
			case *mcp.ListToolsResult:
				if where == "tools" {
					r.Tools = append(r.Tools, nil)
				}
			case *mcp.ListPromptsResult:
				if where == "prompts" {
					r.Prompts = append(r.Prompts, nil)
				}
			case *mcp.ListResourcesResult:
				if where == "resources" {
					r.Resources = append(r.Resources, nil)
				}
			case *mcp.ListResourceTemplatesResult:
				if where == "templates" {
					r.ResourceTemplates = append(r.ResourceTemplates, nil)
				}
			case *mcp.GetPromptResult:
				switch where {
				case "messages":
					r.Messages = nil
				case "poison":
					poison(r)
				}
			case *mcp.ReadResourceResult:
				switch where {
				case "read":
					r.Contents = []*mcp.ResourceContents{nil}
				case "contents":
					r.Contents = nil
				case "poison":
					poison(r)
				}
			case *mcp.CallToolResult:
				if where == "poison" {
					poison(r)
				}
			}
			return res, nil
		}
	}
}

// poison puts on a result what an upstream has no business sending to the
// client: a requestState the gateway never issued, and _meta keys the protocol
// keeps for itself (R-06), next to one key that is the upstream's to use.
func poison(res mcp.Result) {
	switch r := res.(type) {
	case *mcp.CallToolResult:
		r.RequestState = "S3CRET-STATE"
	case *mcp.GetPromptResult:
		r.RequestState = "S3CRET-STATE"
	case *mcp.ReadResourceResult:
		r.RequestState = "S3CRET-STATE"
	}
	res.SetMeta(map[string]any{
		"io.modelcontextprotocol/serverInfo":   map[string]any{"name": "evil", "version": "9"},
		"io.modelcontextprotocol/related-task": map[string]any{"taskId": "t1"},
		"com.example/keep":                     "yes",
	})
}
