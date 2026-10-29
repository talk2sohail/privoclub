package safe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func logger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestRecoverTurnsAPanicIntoAnError(t *testing.T) {
	log, out := logger()
	h := Recover(log, "upstream evil")(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		var p *mcp.ProgressNotificationParams
		_ = p.ProgressToken // the nil dereference of R-01
		return &mcp.ListToolsResult{}, nil
	})
	res, err := h(context.Background(), "notifications/elicitation/complete", nil)
	if res != nil || err == nil || err.Error() != "internal error" {
		t.Fatalf("got (%v, %v), want (nil, \"internal error\")", res, err)
	}
	for _, want := range []string{"notifications/elicitation/complete", "upstream evil", "nil pointer", "recover_test.go"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRecoverPassesResultsAndErrorsThrough(t *testing.T) {
	log, out := logger()
	want := &mcp.ListToolsResult{}
	sentinel := errors.New("refused")
	h := Recover(log, "gateway")(func(_ context.Context, method string, _ mcp.Request) (mcp.Result, error) {
		if method == "fail" {
			return nil, sentinel
		}
		return want, nil
	})
	if res, err := h(context.Background(), "tools/list", nil); res != want || err != nil {
		t.Fatalf("got (%v, %v)", res, err)
	}
	if _, err := h(context.Background(), "fail", nil); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the handler's own error", err)
	}
	if out.Len() != 0 {
		t.Errorf("a normal call logged: %s", out.String())
	}
}

func TestRecoverDoesNotLogTheMessageOrALongPanic(t *testing.T) {
	log, out := logger()
	h := Recover(log, "gateway")(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		panic(strings.Repeat("x", 5000))
	})
	if _, err := h(context.Background(), "tools/call", nil); err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(out.String(), strings.Repeat("x", 300)) {
		t.Errorf("the panic text was not truncated")
	}
}
