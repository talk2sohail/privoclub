// Package safe keeps a bug or a hostile message from ending the process.
//
// One Privo process serves every tenant, and the Go SDK recovers from no panic
// in the goroutines it starts for incoming messages. A nil dereference in one
// of its handlers, reached by a single message from an upstream server (the
// independent review of 2026-10-29, R-01), therefore ended the process for
// everyone, and again after each restart.
package safe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxPanicText bounds the panic value written to the log.
const maxPanicText = 200

// Recover returns receiving middleware that turns a panic while handling a
// message into an error. Install it outermost. The log entry names the method,
// the panic text and the stack, never the message: it can carry credentials or
// tenant data. where says which side of the gateway the message came from.
func Recover(log *slog.Logger, where string) mcp.Middleware {
	if log == nil {
		log = slog.Default()
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			defer func() {
				if p := recover(); p != nil {
					text := fmt.Sprint(p)
					if len(text) > maxPanicText {
						text = text[:maxPanicText] + "..."
					}
					log.Error("recovered from a panic while handling an MCP message",
						"where", where, "method", method, "panic", text, "stack", string(debug.Stack()))
					res, err = nil, errors.New("internal error")
				}
			}()
			return next(ctx, method, req)
		}
	}
}
