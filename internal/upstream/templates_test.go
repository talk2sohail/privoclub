package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/store"
)

func connectInMemory(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	t1, t2 := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestLoadResourceTemplatesAndPrune(t *testing.T) {
	ctx := context.Background()
	dropSecond := false
	srv := mcp.NewServer(&mcp.Implementation{Name: "tpl", Version: "0.0.0"}, nil)
	srv.AddResource(&mcp.Resource{URI: "repo://readme", Name: "readme"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "repo://readme", Text: "ok"}}}, nil
	})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item", Description: "Read an item"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	// Paginate templates across 2 pages via receiving middleware.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "resources/templates/list" {
				return next(ctx, method, req)
			}
			p, _ := req.GetParams().(*mcp.ListResourceTemplatesParams)
			cursor := ""
			if p != nil {
				cursor = p.Cursor
			}
			if cursor == "" {
				res := &mcp.ListResourceTemplatesResult{
					ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "repo://items/{id}", Name: "item", Description: "Read an item"}},
				}
				if !dropSecond {
					res.NextCursor = "p2"
				}
				return res, nil
			}
			return &mcp.ListResourceTemplatesResult{
				ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "repo://files/{+path}", Name: "file", Description: "Read a file"}},
			}, nil
		}
	})

	cs := connectInMemory(t, srv)
	fs := &fakeStore{}
	m := New(Options{Store: fs})
	defer m.Close()
	e := &entry{org: "org-1", srv: store.Server{ID: "srv-1", Alias: "repo"}, done: make(chan struct{})}
	close(e.done)

	if err := m.load(ctx, e, cs); err != nil {
		t.Fatalf("load: %v", err)
	}
	e.mu.Lock()
	st := e.state
	e.mu.Unlock()
	if st.Status != StatusUp || len(st.Items) != 3 {
		t.Fatalf("state = %+v, want up with 3 items (1 resource + 2 templates)", st)
	}

	var tmplPins []store.Pin
	for _, p := range fs.seen {
		if p.Kind == KindResourceTemplate {
			tmplPins = append(tmplPins, p)
		}
	}
	if len(tmplPins) != 2 {
		t.Fatalf("template pins = %+v, want 2", tmplPins)
	}
	wantDef, _ := json.Marshal(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item", Description: "Read an item"})
	sum := sha256.Sum256(wantDef)
	if tmplPins[0].Name != "repo://items/{id}" || tmplPins[0].Hash != hex.EncodeToString(sum[:]) || tmplPins[0].Definition != string(wantDef) {
		t.Fatalf("pin[0] = %+v, want canonical definition %s", tmplPins[0], wantDef)
	}

	// Second load without the second template prunes it from keep.
	dropSecond = true
	if err := m.load(ctx, e, cs); err != nil {
		t.Fatalf("second load: %v", err)
	}
	prunes := fs.pruned()
	if len(prunes) != 2 {
		t.Fatalf("prunes = %d, want 2", len(prunes))
	}
	lastKeep := prunes[1].keep
	for _, k := range lastKeep {
		if k.Kind == KindResourceTemplate && k.Name == "repo://files/{+path}" {
			t.Fatalf("removed template still in keep: %+v", lastKeep)
		}
	}
}

func TestLoadResourceTemplatesMethodNotFoundAndToolsOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("MethodNotFound treated as 0 templates", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "no-tpl", Version: "0.0.0"}, nil)
		srv.AddResource(&mcp.Resource{URI: "repo://readme", Name: "readme"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "resources/templates/list" {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
				}
				return next(ctx, method, req)
			}
		})
		cs := connectInMemory(t, srv)
		fs := &fakeStore{}
		m := New(Options{Store: fs})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "repo"}, done: make(chan struct{})}
		close(e.done)
		if err := m.load(ctx, e, cs); err != nil {
			t.Fatalf("load failed on MethodNotFound: %v", err)
		}
		e.mu.Lock()
		st := e.state
		e.mu.Unlock()
		if st.Status != StatusUp || len(st.Items) != 1 || st.Items[0].Kind != KindResource {
			t.Fatalf("state = %+v, want up with 1 resource", st)
		}
	})

	t.Run("tools-only server is never asked for templates", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "tools-only", Version: "0.0.0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
		asked := false
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "resources/templates/list" {
					asked = true
				}
				return next(ctx, method, req)
			}
		})
		cs := connectInMemory(t, srv)
		m := New(Options{Store: &fakeStore{}})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "only"}, done: make(chan struct{})}
		close(e.done)
		if err := m.load(ctx, e, cs); err != nil {
			t.Fatal(err)
		}
		if asked {
			t.Fatal("tools-only server was asked for resources/templates/list")
		}
	})
}

func TestLoadResourceTemplatesFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("null entry fails load", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "null-tpl", Version: "0.0.0"}, nil)
		srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				res, err := next(ctx, method, req)
				if err != nil {
					return nil, err
				}
				if r, ok := res.(*mcp.ListResourceTemplatesResult); ok {
					r.ResourceTemplates = append(r.ResourceTemplates, nil)
				}
				return res, nil
			}
		})
		cs := connectInMemory(t, srv)
		m := New(Options{Store: &fakeStore{}})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "evil"}, done: make(chan struct{})}
		close(e.done)
		err := m.load(ctx, e, cs)
		if err == nil || !strings.Contains(err.Error(), "resources/templates/list") || !strings.Contains(err.Error(), "null") {
			t.Fatalf("err = %v, want null-entry error for resources/templates/list", err)
		}
	})

	t.Run("endless pagination stops at MaxPages", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "page-tpl", Version: "0.0.0"}, nil)
		srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
		n := 0
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method != "resources/templates/list" {
					return next(ctx, method, req)
				}
				n++
				return &mcp.ListResourceTemplatesResult{
					NextCursor:        fmt.Sprintf("c%d", n),
					ResourceTemplates: []*mcp.ResourceTemplate{},
				}, nil
			}
		})
		cs := connectInMemory(t, srv)
		m := New(Options{Store: &fakeStore{}, MaxPages: 3})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "evil"}, done: make(chan struct{})}
		close(e.done)
		err := m.load(ctx, e, cs)
		if err == nil || !strings.Contains(err.Error(), "resources/templates/list") || !strings.Contains(err.Error(), "pages") {
			t.Fatalf("err = %v, want max pages error", err)
		}
	})

	t.Run("MethodNotFound on later page still fails closed", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "p2-err", Version: "0.0.0"}, nil)
		srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{}, nil
		})
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method != "resources/templates/list" {
					return next(ctx, method, req)
				}
				p, _ := req.GetParams().(*mcp.ListResourceTemplatesParams)
				if p == nil || p.Cursor == "" {
					return &mcp.ListResourceTemplatesResult{NextCursor: "p2", ResourceTemplates: []*mcp.ResourceTemplate{}}, nil
				}
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "gone"}
			}
		})
		cs := connectInMemory(t, srv)
		m := New(Options{Store: &fakeStore{}})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "evil"}, done: make(chan struct{})}
		close(e.done)
		err := m.load(ctx, e, cs)
		var rpc *jsonrpc.Error
		if !errors.As(err, &rpc) || rpc.Code != jsonrpc.CodeMethodNotFound {
			t.Fatalf("err = %v, want MethodNotFound on page 2 to fail closed", err)
		}
	})

	t.Run("definition budget caps templates", func(t *testing.T) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "big-tpl", Version: "0.0.0"}, nil)
		srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "repo://items/{id}", Name: "item", Description: strings.Repeat("x", 1000)},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{}, nil
			})
		cs := connectInMemory(t, srv)
		m := New(Options{Store: &fakeStore{}, MaxLoadBytes: 500})
		defer m.Close()
		e := &entry{org: "o", srv: store.Server{ID: "s1", Alias: "big"}, done: make(chan struct{})}
		close(e.done)
		err := m.load(ctx, e, cs)
		if err == nil || !strings.Contains(err.Error(), "pass 500 bytes") {
			t.Fatalf("err = %v, want budget overflow error", err)
		}
	})
}
