//go:build acceptance

// Command echoserver is a stdio MCP server used only by the acceptance suite.
//
// Modes:
//
//	default     tool "echo", prompt "greet", resource "echo://note"
//	tools-only  tool "ping" only; writes any prompts/* or resources/* request to $FLAG
//	flaky       exits with status 1 until it has been started twice (counts in $MARKER), then default
//	annotated   tools "read_note" (readOnlyHint) and "delete_note" (destructiveHint)
package main

import (
	"context"
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
	mode := flag.String("mode", "default", "default, tools-only, flaky or annotated")
	flag.Parse()

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

	s := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "0.0.1"}, nil)

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

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
