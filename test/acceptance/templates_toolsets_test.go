//go:build acceptance

package acceptance

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosida95/uritemplate/v3"
)

func TestResourceTemplatesEndToEnd(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", []string{"-mode", "templates"}, nil)

	ctx := context.Background()
	s := g.connect(g.agent)

	// Before review, templates are hidden and cannot be read.
	listed, err := s.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.ResourceTemplates) != 0 {
		t.Fatalf("pending templates listed before review: %+v", listed.ResourceTemplates)
	}
	if _, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://echo/echo://items/42"}); err == nil {
		t.Fatal("pending template read succeeded before review")
	}

	// Accept only the first two templates at first, leaving search pending.
	pinsRes := g.api(http.MethodGet, "/api/v1/servers/echo/pins", g.owner, nil, http.StatusOK)
	rawPins, _ := pinsRes["pins"].([]any)
	var acceptSome []map[string]any
	for _, p := range rawPins {
		m, _ := p.(map[string]any)
		if m["status"] != "pending" {
			continue
		}
		if m["kind"] == "resource_template" && m["name"] == "echo://search{?q,limit}" {
			continue
		}
		acceptSome = append(acceptSome, map[string]any{"kind": m["kind"], "name": m["name"], "hash": m["hash"]})
	}
	g.api(http.MethodPost, "/api/v1/servers/echo/pins:accept", g.owner, map[string]any{"pins": acceptSome}, http.StatusOK)

	s2 := g.connect(g.agent)
	listed, err = s2.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rt := range listed.ResourceTemplates {
		got = append(got, rt.URITemplate)
	}
	slices.Sort(got)
	want := []string{"privo://echo/echo://files/{+path}", "privo://echo/echo://items/{id}"}
	if !slices.Equal(got, want) {
		t.Fatalf("listed templates = %v, want %v", got, want)
	}

	// Client expands the RFC 6570 templates directly and reads them.
	for _, tc := range []struct {
		tmplStr  string
		vars     uritemplate.Values
		wantText string
		wantUp   string
	}{
		{
			tmplStr:  "privo://echo/echo://items/{id}",
			vars:     uritemplate.Values{"id": uritemplate.String("42")},
			wantText: "tpl:echo://items/42",
			wantUp:   "echo://items/42",
		},
		{
			tmplStr:  "privo://echo/echo://files/{+path}",
			vars:     uritemplate.Values{"path": uritemplate.String("docs/readme.md")},
			wantText: "tpl:echo://files/docs/readme.md",
			wantUp:   "echo://files/docs/readme.md",
		},
	} {
		tmpl, err := uritemplate.New(tc.tmplStr)
		if err != nil {
			t.Fatalf("invalid rewritten template %q: %v", tc.tmplStr, err)
		}
		expanded, err := tmpl.Expand(tc.vars)
		if err != nil {
			t.Fatalf("expand %q: %v", tc.tmplStr, err)
		}
		res, err := s2.ReadResource(ctx, &mcp.ReadResourceParams{URI: expanded})
		if err != nil {
			t.Fatalf("ReadResource(%q): %v", expanded, err)
		}
		if len(res.Contents) != 1 || res.Contents[0].Text != tc.wantText {
			t.Fatalf("ReadResource(%q) = %+v, want text %q", expanded, res.Contents, tc.wantText)
		}
		if wantURI := "privo://echo/" + url.PathEscape(tc.wantUp); res.Contents[0].URI != wantURI {
			t.Fatalf("rewritten URI = %q, want %q", res.Contents[0].URI, wantURI)
		}
	}

	// Unaccepted search template and traversal paths are refused.
	for _, bad := range []string{
		"privo://echo/echo://search?q=privo&limit=5",
		"privo://echo/echo://files/../../etc/passwd",
		"privo://echo/echo://items/1/extra",
	} {
		if _, err := s2.ReadResource(ctx, &mcp.ReadResourceParams{URI: bad}); err == nil {
			t.Errorf("ReadResource(%q) succeeded, want error", bad)
		}
	}

	// Accept the remaining search template and test query expansion + policy deny.
	g.acceptPins("echo")
	s3 := g.connect(g.agent)
	res, err := s3.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://echo/echo://search?q=privo&limit=5"})
	if err != nil || len(res.Contents) != 1 || res.Contents[0].Text != "tpl:echo://search?q=privo&limit=5" {
		t.Fatalf("search template read = %+v, %v", res, err)
	}
	wantCall(g, "echo", "echo://search{?q,limit}", "resources/read", "allow", "ok")

	// A deny rule on the template pattern blocks reads and writes an audit refusal.
	g.api(http.MethodPost, "/api/v1/policies", g.owner, map[string]any{
		"server": "echo", "tool": "echo://items/{id}", "action": "deny",
	}, http.StatusCreated)
	if _, err := s3.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://echo/echo://items/99"}); err == nil || !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("policy-denied template read = %v, want Denied", err)
	}
	wantCall(g, "echo", "echo://items/{id}", "resources/read", "deny", "denied")
}

func TestToolsetsForPromptsAndResources(t *testing.T) {
	gh := newAcceptanceGitHub(t)
	g := webGateway(t, gh)
	boss := ownerOf(t, g, gh)
	for _, team := range []string{"Designers", "Developers"} {
		if code, out := boss.app(http.MethodPost, "/v1/teams", map[string]any{"name": team}, nil); code != http.StatusCreated {
			t.Fatalf("team %s: %d %v", team, code, out)
		}
	}
	addReviewed(t, g, boss, map[string]any{
		"alias":     "kit",
		"transport": "stdio",
		"command":   map[string]any{"path": echoBin, "sha256": digest(t, echoBin), "args": []string{"-mode", "templates"}},
		"teams":     []string{"designers", "developers"},
	})

	ada, adaToken := join(t, g, gh, boss, "ada", 2)
	_, bobToken := join(t, g, gh, boss, "bob", 3)
	_, carolToken := join(t, g, gh, boss, "carol", 4)
	for _, m := range [][2]string{{"designers", "ada"}, {"designers", "carol"}, {"developers", "bob"}} {
		if code, out := boss.app(http.MethodPost, "/v1/teams/"+m[0]+"/members", map[string]any{"person": m[1]}, nil); code != http.StatusOK {
			t.Fatalf("member %v: %d %v", m, code, out)
		}
	}

	// Narrow designers across prompts, resources and templates, leaving developers open.
	if code, out := boss.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{
		"prompts":            []string{"greet"},
		"resources":          []string{"echo://note"},
		"resource_templates": []string{"echo://items/{id}"},
	}, nil); code != http.StatusOK {
		t.Fatalf("narrow per-kind: %d %v", code, out)
	}

	// Managers see team_prompts, team_resources, team_templates; members do not, and members get 403 on PATCH.
	_, bossServers := boss.app(http.MethodGet, "/v1/servers", nil, nil)
	for _, s := range list(bossServers, "servers") {
		if s["alias"] == "kit" {
			if s["team_prompts"] == nil || s["team_resources"] == nil || s["team_templates"] == nil {
				t.Fatalf("owner missing per-kind toolset config: %v", s)
			}
		}
	}
	_, adaServers := ada.app(http.MethodGet, "/v1/servers", nil, nil)
	for _, s := range list(adaServers, "servers") {
		if s["alias"] == "kit" {
			for _, k := range []string{"team_tools", "team_prompts", "team_resources", "team_templates"} {
				if s[k] != nil {
					t.Fatalf("member sees %s: %v", k, s[k])
				}
			}
			items, _ := s["items"].(map[string]any)
			if items["prompt"] != float64(1) || items["resource"] != float64(1) || items["resource_template"] != float64(1) {
				t.Fatalf("member item counts leaked hidden items: %v", items)
			}
		}
	}
	if code, _ := ada.app(http.MethodPatch, "/v1/servers/kit/teams/designers", map[string]any{"prompts": nil}, nil); code != http.StatusForbidden {
		t.Fatalf("member PATCH status = %d, want 403", code)
	}

	ctx := context.Background()
	adaClient, bobClient := g.connect(adaToken), g.connect(bobToken)

	// Designers see only chosen prompt, resource and template; developers still see all.
	g.eventually("ada sees only greet prompt", 6*time.Second, func() bool {
		pr, err := adaClient.ListPrompts(ctx, &mcp.ListPromptsParams{})
		return err == nil && len(pr.Prompts) == 1 && pr.Prompts[0].Name == "kit__greet"
	})
	rr, err := adaClient.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil || len(rr.Resources) != 1 || rr.Resources[0].Name != "kit__note" {
		t.Fatalf("ada resources = %+v, %v", rr, err)
	}
	tr, err := adaClient.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{})
	if err != nil || len(tr.ResourceTemplates) != 1 || tr.ResourceTemplates[0].URITemplate != "privo://kit/echo://items/{id}" {
		t.Fatalf("ada templates = %+v, %v", tr, err)
	}

	if bobPr, err := bobClient.ListPrompts(ctx, &mcp.ListPromptsParams{}); err != nil || len(bobPr.Prompts) != 2 {
		t.Fatalf("bob prompts = %+v, %v", bobPr, err)
	}
	if bobRr, err := bobClient.ListResources(ctx, &mcp.ListResourcesParams{}); err != nil || len(bobRr.Resources) != 2 {
		t.Fatalf("bob resources = %+v, %v", bobRr, err)
	}
	if bobTr, err := bobClient.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{}); err != nil || len(bobTr.ResourceTemplates) != 3 {
		t.Fatalf("bob templates = %+v, %v", bobTr, err)
	}

	// Visible items work for ada.
	if got, err := adaClient.GetPrompt(ctx, &mcp.GetPromptParams{Name: "kit__greet", Arguments: map[string]string{"name": "Ada"}}); err != nil || got.Messages[0].Content.(*mcp.TextContent).Text != "Hello Ada" {
		t.Fatalf("ada allowed prompt = %+v, %v", got, err)
	}
	if got, err := adaClient.ReadResource(ctx, &mcp.ReadResourceParams{URI: "privo://kit/echo://items/7"}); err != nil || got.Contents[0].Text != "tpl:echo://items/7" {
		t.Fatalf("ada allowed template = %+v, %v", got, err)
	}

	// Hidden prompt answers identically to unknown prompt.
	_, hiddenPromptErr := adaClient.GetPrompt(ctx, &mcp.GetPromptParams{Name: "kit__farewell", Arguments: map[string]string{"name": "Ada"}})
	_, unknownPromptErr := adaClient.GetPrompt(ctx, &mcp.GetPromptParams{Name: "kit__nope", Arguments: map[string]string{"name": "Ada"}})
	if hiddenPromptErr == nil || unknownPromptErr == nil ||
		strings.ReplaceAll(hiddenPromptErr.Error(), "kit__farewell", "X") != strings.ReplaceAll(unknownPromptErr.Error(), "kit__nope", "X") {
		t.Fatalf("hidden prompt err %v vs unknown %v", hiddenPromptErr, unknownPromptErr)
	}

	// Hidden static resource and hidden template answer identically to unknown resource.
	// Use a second designer member (carol) so Gateway's 1-minute per-(caller, server, reason)
	// hidden-item audit window captures both prompts/get (ada) and resources/read (carol).
	carolClient := g.connect(carolToken)
	hiddenStaticURI := "privo://kit/" + url.PathEscape("echo://secret")
	unknownURI := "privo://kit/" + url.PathEscape("echo://nope")
	_, hiddenStaticErr := carolClient.ReadResource(ctx, &mcp.ReadResourceParams{URI: hiddenStaticURI})
	_, unknownResErr := carolClient.ReadResource(ctx, &mcp.ReadResourceParams{URI: unknownURI})
	if hiddenStaticErr == nil || unknownResErr == nil ||
		strings.ReplaceAll(hiddenStaticErr.Error(), hiddenStaticURI, "X") != strings.ReplaceAll(unknownResErr.Error(), unknownURI, "X") {
		t.Fatalf("hidden static err %v vs unknown %v", hiddenStaticErr, unknownResErr)
	}
	hiddenTmplURI := "privo://kit/echo://files/a/b"
	_, hiddenTmplErr := adaClient.ReadResource(ctx, &mcp.ReadResourceParams{URI: hiddenTmplURI})
	if hiddenTmplErr == nil ||
		strings.ReplaceAll(hiddenTmplErr.Error(), hiddenTmplURI, "X") != strings.ReplaceAll(unknownResErr.Error(), unknownURI, "X") {
		t.Fatalf("hidden tmpl err %v vs unknown %v", hiddenTmplErr, unknownResErr)
	}

	// Audit records denied:not_in_toolset for hidden prompt and hidden resource.
	g.eventually("hidden prompt and resource audited as denied:not_in_toolset", 6*time.Second, func() bool {
		_, out := boss.app(http.MethodGet, "/v1/audit/events?limit=200&decision=denied:not_in_toolset", nil, nil)
		evs := list(out, "events")
		var gotPrompt, gotRes bool
		for _, ev := range evs {
			if ev["action"] == "prompts/get" && ev["item"] == "farewell" {
				gotPrompt = true
			}
			if ev["action"] == "resources/read" {
				gotRes = true
			}
		}
		return gotPrompt && gotRes
	})
}
