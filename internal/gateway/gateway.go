// Package gateway serves the MCP endpoint (ADR 0001, 0004).
//
// It is implemented as receiving middleware on one stateless MCP server: list
// requests are answered from the upstream manager's view (only reviewed items
// are shown) and calls are routed through the pipeline: resolve, pin check,
// freeze arguments, decide, write the audit intent, call upstream, write the
// completion. Policies, approvals and transform hooks plug into the decide
// stage in later features.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/argcheck"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/safe"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/vault"
)

// Settings keys.
const (
	SettingRecordArguments = "audit.record_arguments"
	SettingRecordResults   = "audit.record_results"
)

// SettingPolicyDefault is the org default action when no rule matches.
const SettingPolicyDefault = "policy.default"

// SettingPolicyFlowWindow is the per-org cross-server flow window (ADR 0032).
const SettingPolicyFlowWindow = "policy.flow_window"

// Store is the store subset the gateway uses.
type Store interface {
	PinStatuses(ctx context.Context, org store.OrgID, serverID string) (map[store.PinKey]string, error)
	Setting(ctx context.Context, org store.OrgID, key, def string) (string, error)
	ListPolicies(ctx context.Context, org store.OrgID) ([]store.Policy, error)
	OrgFlowTags(ctx context.Context, org store.OrgID) ([]store.FlowTag, error)
	UserByID(ctx context.Context, org store.OrgID, id string) (store.User, error)
	AccessFor(ctx context.Context, org store.OrgID, userID string) (store.Access, error)
}

// Gateway is the MCP data plane.
type Gateway struct {
	Store    Store
	Upstream *upstream.Manager
	Keys     *keyring.Keyring
	Audit    *audit.Writer
	Auth     *auth.Authenticator
	Approval *approval.Service
	Logger   *slog.Logger
	// BaseURL is the public base used in approval links.
	BaseURL string
	// ResourceMetadataURL is advertised in 401 responses (RFC 9728).
	ResourceMetadataURL string
	// Scopes are named in 401 responses when set, so an OAuth client knows
	// what to ask for (ADR 0022 point 5). Every accepted credential has them.
	Scopes []string
	// CallTimeout is how long one call to an upstream server may take; zero
	// means no limit (R-04). It starts once the call is allowed and approved.
	CallTimeout time.Duration
	// Telemetry records how each call went (ADR 0024); nil records nothing.
	Telemetry *telemetry.Recorder
	// Flow tracks recent untrusted_source reads per caller (ADR 0032);
	// lazily initialized when nil.
	Flow *policy.FlowTracker

	flowOnce    sync.Once
	flowDefault *policy.FlowTracker

	hiddenMu sync.Mutex
	hidden   map[string]time.Time // last record of a hidden call, per caller, server and reason

	args argcheck.Checker // compiled input schemas of the tools called
}

func (g *Gateway) flowTracker() *policy.FlowTracker {
	if g.Flow != nil {
		return g.Flow
	}
	g.flowOnce.Do(func() {
		g.flowDefault = policy.NewFlowTracker()
	})
	return g.flowDefault
}

// Handler returns the authenticated MCP HTTP handler.
func (g *Gateway) Handler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "privo", Version: buildinfo.Version}, &mcp.ServerOptions{
		Instructions: "Privo gateway. Tool and prompt names are <server>__<name>; resource URIs are privo://<server>/<id>.",
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{ListChanged: true},
			Prompts:   &mcp.PromptCapabilities{ListChanged: true},
			Resources: &mcp.ResourceCapabilities{ListChanged: true},
		},
		Logger: g.Logger,
	})
	// Outermost, so a panic anywhere in the gateway answers one request with an error.
	server.AddReceivingMiddleware(safe.Recover(g.Logger, "gateway"), g.middleware)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, Logger: g.Logger})
	return mcpauth.RequireBearerToken(g.Auth.MCPVerifier(), &mcpauth.RequireBearerTokenOptions{
		ResourceMetadataURL: g.ResourceMetadataURL,
		Scopes:              g.Scopes,
	})(h)
}

func (g *Gateway) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		var p auth.Principal
		if extra := req.GetExtra(); extra != nil {
			p, _ = auth.FromTokenInfo(extra.TokenInfo)
		}
		if p.OrgID == "" {
			return next(ctx, method, req) // initialize, ping and similar
		}
		switch method {
		case "tools/list":
			return g.listTools(ctx, p)
		case "tools/call":
			params, _ := req.GetParams().(*mcp.CallToolParamsRaw)
			if params == nil {
				return nil, invalidParams("invalid tools/call params")
			}
			res, err := g.callTool(ctx, p, params)
			return g.finish(req, res, err)
		case "prompts/list":
			return g.listPrompts(ctx, p)
		case "prompts/get":
			params, _ := req.GetParams().(*mcp.GetPromptParams)
			if params == nil {
				return nil, invalidParams("invalid prompts/get params")
			}
			res, err := g.getPrompt(ctx, p, params)
			return g.finish(req, res, err)
		case "resources/list":
			return g.listResources(ctx, p)
		case "resources/templates/list":
			return &mcp.ListResourceTemplatesResult{Cacheable: listCache(), ResourceTemplates: []*mcp.ResourceTemplate{}}, nil
		case "resources/read":
			params, _ := req.GetParams().(*mcp.ReadResourceParams)
			if params == nil {
				return nil, invalidParams("invalid resources/read params")
			}
			res, err := g.readResource(ctx, p, params)
			return g.finish(req, res, err)
		}
		return next(ctx, method, req)
	}
}

// finish prepares the result of tools/call, prompts/get or resources/read for
// the wire, or the error with a JSON-RPC code.
func (g *Gateway) finish(req mcp.Request, res mcp.Result, err error) (mcp.Result, error) {
	res, err = withResultType(req, res, err)
	return res, g.rpcError(err)
}

// Access returns what a caller reaches (ADR 0020): the servers of their
// teams. The caller's member is the principal's user id: a signed-in person,
// or the member a token belongs to (F27). A token of nobody is in "everyone"
// only, whatever person it names. A removed member reaches nothing. Any error
// is returned, so callers fail closed. The admin API uses it too, so both
// answer the same.
func Access(ctx context.Context, st Store, p auth.Principal) (store.Access, error) {
	return st.AccessFor(ctx, p.OrgID, p.UserID)
}

// access is Access for a call through the gateway. A member's own token
// works only while one of the member's current roles uses the gateway (ADR
// 0021): a person who is now only an approver reaches nothing with it.
func (g *Gateway) access(ctx context.Context, p auth.Principal) (store.Access, error) {
	acc, err := Access(ctx, g.Store, p)
	if err != nil {
		return store.Access{}, err
	}
	if p.UserID != "" && !auth.RolesCan(acc.Roles, auth.PermUseGateway) {
		return store.Access{Servers: map[string]bool{}}, nil
	}
	return acc, nil
}

// CallerRoles are the roles rules see (ADR 0025): a member's current roles,
// read with their access, or the token's own role when no member is behind it.
// The admin API uses it to check a grant against the rules (F10).
func CallerRoles(p auth.Principal, acc store.Access) []string {
	if p.UserID != "" {
		return acc.Roles
	}
	return p.Roles
}

// visible returns the reviewed items of kind on the servers p reaches,
// sorted by public name.
func (g *Gateway) visible(ctx context.Context, p auth.Principal, kind string) ([]upstream.Item, error) {
	org := p.OrgID
	acc, err := g.access(ctx, p)
	if err != nil {
		return nil, err
	}
	items := g.Upstream.Items(org)
	statuses := map[string]map[store.PinKey]string{}
	var out []upstream.Item
	for _, it := range items {
		if it.Kind != kind || !acc.Reaches(it.ServerID) {
			continue
		}
		// A team may see only some of a server's tools (ADR 0026); a hidden
		// tool is left out here, so a call on it is answered like an unknown name.
		if it.Kind == upstream.KindTool && !acc.SeesTool(it.ServerID, it.Name) {
			continue
		}
		st, ok := statuses[it.ServerID]
		if !ok {
			var err error
			if st, err = g.Store.PinStatuses(ctx, org, it.ServerID); err != nil {
				return nil, err
			}
			statuses[it.ServerID] = st
		}
		if st[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] == store.PinAccepted {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return PublicName(out[i].Alias, out[i].Name) < PublicName(out[j].Alias, out[j].Name)
	})
	return dropCollisions(out, g.Logger), nil
}

func (g *Gateway) find(ctx context.Context, p auth.Principal, kind, public string) (upstream.Item, bool, error) {
	items, err := g.visible(ctx, p, kind)
	if err != nil {
		return upstream.Item{}, false, err
	}
	for _, it := range items {
		if PublicName(it.Alias, it.Name) == public {
			return it, true, nil
		}
	}
	return upstream.Item{}, false, nil
}

// Cache hints (ADR 0001). What a person sees depends on who they are, so every
// result is private to the requesting client. Lists may be reused for a few
// seconds, in line with the 5-second bound on revocation (ADR 0003); a resource
// read is never reused. The SDK fills in a default only for results it builds
// itself, and strict clients such as Claude Code reject an empty cacheScope, so
// every list result built here and every read forwarded from an upstream sets
// the hints explicitly. An upstream's own hints are overridden: a "public"
// scope from a server must not make per-person content shareable.
const listCacheTTLMs = 5000

func listCache() mcp.Cacheable { return mcp.Cacheable{TTLMs: listCacheTTLMs, CacheScope: "private"} }
func readCache() mcp.Cacheable { return mcp.Cacheable{TTLMs: 0, CacheScope: "private"} }

func (g *Gateway) listTools(ctx context.Context, p auth.Principal) (mcp.Result, error) {
	items, err := g.visible(ctx, p, upstream.KindTool)
	if err != nil {
		return nil, err
	}
	res := &mcp.ListToolsResult{Cacheable: listCache(), Tools: make([]*mcp.Tool, 0, len(items))}
	for _, it := range items {
		t := *it.Tool
		t.Name = PublicName(it.Alias, it.Name)
		res.Tools = append(res.Tools, &t)
	}
	return res, nil
}

func (g *Gateway) listPrompts(ctx context.Context, p auth.Principal) (mcp.Result, error) {
	items, err := g.visible(ctx, p, upstream.KindPrompt)
	if err != nil {
		return nil, err
	}
	res := &mcp.ListPromptsResult{Cacheable: listCache(), Prompts: make([]*mcp.Prompt, 0, len(items))}
	for _, it := range items {
		pr := *it.Prompt
		pr.Name = PublicName(it.Alias, it.Name)
		res.Prompts = append(res.Prompts, &pr)
	}
	return res, nil
}

func (g *Gateway) listResources(ctx context.Context, p auth.Principal) (mcp.Result, error) {
	items, err := g.visible(ctx, p, upstream.KindResource)
	if err != nil {
		return nil, err
	}
	res := &mcp.ListResourcesResult{Cacheable: listCache(), Resources: make([]*mcp.Resource, 0, len(items))}
	for _, it := range items {
		r := *it.Resource
		r.Name = PublicName(it.Alias, it.Resource.Name)
		r.URI = ResourceURI(it.Alias, it.Name)
		res.Resources = append(res.Resources, &r)
	}
	return res, nil
}

// call is one routed request through the pipeline.
type call struct {
	p      auth.Principal
	item   upstream.Item
	action string
	args   json.RawMessage
}

// refusal is a call stopped by policy or by a person; its message is shown
// to the model as a tool error.
type refusal struct {
	msg   string
	class string // the error class of the call statistic (ADR 0024)
}

func (r *refusal) Error() string { return r.msg }

// decide runs the decision stage: rules for tool calls, then an approval when
// a rule asks for one. It returns the label written to the audit log.
// decision is the outcome of the decision stage.
type decision struct {
	label      string // written to the audit log: allow or approved:<id>
	approvalID string // set when an approval was consumed
	approvedBy string
	waited     time.Duration // held for a person's decision (ADR 0024, review T-10)
	isSource   bool          // true when the item carries untrusted_source (ADR 0032)
}

func flowCallerKey(p auth.Principal) string {
	if p.UserID != "" {
		return "user:" + p.UserID
	}
	if p.TokenID != "" {
		return "token:" + p.TokenID
	}
	return ""
}

func itemFlowTags(tags []store.FlowTag, serverID, itemName string) (isSource, isSink bool) {
	for _, t := range tags {
		if t.ServerID != serverID {
			continue
		}
		if t.ToolName != "" && t.ToolName != itemName {
			continue
		}
		switch t.Tag {
		case store.FlowTagUntrustedSource:
			isSource = true
		case store.FlowTagEgressSink:
			isSink = true
		}
	}
	return isSource, isSink
}

func (g *Gateway) flowWindow(ctx context.Context, org store.OrgID) (time.Duration, error) {
	raw, err := g.Store.Setting(ctx, org, SettingPolicyFlowWindow, "")
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return policy.DefaultFlowWindow, nil
	}
	d, err := policy.ParseFlowWindow(raw)
	if err != nil {
		if g.Logger != nil {
			g.Logger.Warn("invalid policy.flow_window setting; using default", "org", org, "err", err)
		}
		return policy.DefaultFlowWindow, nil
	}
	return d, nil
}

func (g *Gateway) decide(ctx context.Context, c call, digest, callID string) (decision, error) {
	rules, err := g.Store.ListPolicies(ctx, c.p.OrgID)
	if err != nil {
		return decision{}, err
	}
	def, err := g.Store.Setting(ctx, c.p.OrgID, SettingPolicyDefault, store.ActionAllow)
	if err != nil {
		return decision{}, err
	}
	acc, err := g.access(ctx, c.p)
	if err != nil {
		return decision{}, err
	}
	tags, err := g.Store.OrgFlowTags(ctx, c.p.OrgID)
	if err != nil {
		return decision{}, err
	}
	var ann *mcp.ToolAnnotations
	if c.item.Tool != nil {
		ann = c.item.Tool.Annotations
	}
	// Rules match tools, prompts and resources alike by upstream name, so a
	// server-wide deny or a default deny covers everything the server offers.
	dec := policy.Evaluate(rules, policy.Request{Roles: CallerRoles(c.p, acc), TokenID: c.p.TokenID, UserID: c.p.UserID, Teams: acc.Teams, Server: c.item.Alias, Tool: c.item.Name,
		PinHash: c.item.Hash, Annotations: ann, Args: c.args}, def, time.Now())
	isSource, isSink := itemFlowTags(tags, c.item.ServerID, c.item.Name)
	if isSink && dec.Action == store.ActionAllow {
		win, err := g.flowWindow(ctx, c.p.OrgID)
		if err != nil {
			return decision{}, err
		}
		callerKey := flowCallerKey(c.p)
		tainted, source, age := true, "unidentified caller", time.Duration(0)
		if callerKey != "" {
			tainted, source, age = g.flowTracker().CheckSink(string(c.p.OrgID), callerKey, c.p.Client, win, time.Now())
		}
		if tainted {
			dec.Action = store.ActionAsk
			dec.RuleID = ""
			dec.Reason = fmt.Sprintf("cross-server flow: read from %s %s ago", source, age.Round(time.Second))
		}
	}
	switch dec.Action {
	case store.ActionAllow:
		return decision{label: store.ActionAllow, isSource: isSource}, nil
	case store.ActionDeny:
		g.recordRefusal(ctx, c, callID, store.ActionDeny, "denied:"+dec.Reason)
		// The model gets no rule details: they would help it probe the policy.
		return decision{}, &refusal{msg: "Denied by your organisation's Privo policy.", class: "denied"}
	}
	if c.action != "tools/call" {
		// Approvals cover tool calls; for prompts and resources "ask" fails closed.
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "denied:approval not supported for "+c.action)
		return decision{}, &refusal{msg: "Denied by your organisation's Privo policy.", class: "denied"}
	}
	if g.Approval == nil {
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "approvals_unavailable")
		return decision{}, &refusal{msg: "This call needs approval, but approvals are not configured.", class: "approvals_unavailable"}
	}
	gated := time.Now()
	out, err := g.Approval.Gate(ctx, approval.Request{Org: c.p.OrgID, RequesterID: c.p.TokenID, RequesterName: c.p.Name,
		RequesterPerson: c.p.Person, RequesterUserID: c.p.UserID, ServerID: c.item.ServerID, ServerAlias: c.item.Alias, Tool: c.item.Name, ArgsDigest: digest,
		Args: c.args, Reason: dec.Reason, RuleID: dec.RuleID, PinHash: c.item.Hash})
	if errors.Is(err, approval.ErrTooManyPending) {
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "too_many_pending")
		return decision{}, &refusal{msg: "Too many of your calls are already waiting for approval. Wait for decisions before asking again.", class: "too_many_pending"}
	}
	if err != nil {
		return decision{}, err
	}
	switch out.Result {
	case approval.Approved:
		return decision{label: "approved:" + out.ID, approvalID: out.ID, approvedBy: out.DecidedBy, waited: time.Since(gated), isSource: isSource}, nil
	case approval.Withdrawn:
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "withdrawn_by_requester")
		return decision{}, &refusal{msg: "This request was withdrawn in Privo. Call again to ask for approval anew."}
	case approval.Denied:
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "denied_by_approver")
		return decision{}, &refusal{msg: "A person denied this exact call. Do not retry it unchanged.", class: "denied_by_approver"}
	}
	g.recordRefusal(ctx, c, callID, store.ActionAsk, "pending_approval")
	return decision{}, &refusal{msg: fmt.Sprintf("Approval required: request %s is waiting for a person in Privo (%s/approvals/%s). "+
		"Tell the user, then retry exactly the same call with the same arguments once it is approved. It expires at %s.",
		out.ID, g.BaseURL, out.ID, out.ExpiresAt.UTC().Format("15:04 UTC")), class: "pending_approval"}
}

// checkArgs checks a tool call's canonical arguments against the input schema
// of the definition the gateway serves, which is the accepted one (F02). It
// returns nil for anything but a tool call. The reason of a mismatch can
// repeat argument values: it goes back to the caller and nowhere else.
func (g *Gateway) checkArgs(c call) *refusal {
	if c.action != "tools/call" || c.item.Tool == nil {
		return nil
	}
	err := g.args.Check(c.item.Hash, c.item.Tool.InputSchema, c.args)
	if err == nil {
		return nil
	}
	var m *argcheck.MismatchError
	switch {
	case errors.As(err, &m):
		return &refusal{class: "invalid_arguments", msg: "The arguments do not match this tool's input schema, so Privo did not send the call: " +
			m.Reason + ". Correct the arguments and call again."}
	case errors.Is(err, argcheck.ErrTooCostly):
		return &refusal{class: "arguments_too_costly", msg: "These arguments are too large for Privo to check against this tool's input schema, " +
			"so it did not send the call. Send smaller arguments."}
	}
	// One warning a minute per tool: any caller can repeat the call.
	if g.firstInWindow(string(c.p.OrgID) + "\x00schema\x00" + c.item.ServerID + "\x00" + c.item.Name) {
		g.Telemetry.Diagnose(c.p.OrgID, store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryUpstream, Server: c.item.Alias,
			Message: fmt.Sprintf("the input schema of tool %q cannot be read, so calls to it are refused", c.item.Name)})
	}
	return &refusal{class: "schema_unreadable", msg: fmt.Sprintf("Privo cannot read this tool's input schema, so it cannot check the arguments and refused the call. "+
		"Ask an administrator to look at the server %q in Privo.", c.item.Alias)}
}

// recordRefusal writes an intent and its completion for a call that did not run.
func (g *Gateway) recordRefusal(ctx context.Context, c call, callID, decision, outcome string) {
	now := time.Now()
	ev := store.AuditEvent{ID: ids.New(), CallID: callID, Time: now, Actor: c.p.Name, ActorID: c.p.TokenID, UserID: c.p.UserID, Client: c.p.Client, Server: c.item.Alias,
		Item: c.item.Name, Action: c.action, Decision: decision}
	if err := g.Audit.Intent(ctx, c.p.OrgID, ev); err != nil {
		g.Logger.Error("audit refusal", "err", err)
		return
	}
	ev.ID, ev.Outcome, ev.Time = ids.New(), outcome, time.Now()
	if err := g.Audit.Completion(ctx, c.p.OrgID, ev); err != nil {
		g.Logger.Error("audit refusal completion", "err", err)
	}
}

// run executes the pipeline stages around do and records the outcome: the
// audit (who did what, ADR 0006) and the call statistic (how it went, ADR
// 0024). do returns the outcome and the result it relays.
func (g *Gateway) run(ctx context.Context, c call, do func(context.Context, *mcp.ClientSession) (outcome string, result any, err error)) error {
	callID := ids.New()
	arrived := time.Now()
	st := store.CallStat{ID: callID, Time: arrived, UserID: c.p.UserID, TokenID: c.p.TokenID, Client: c.p.Client, Server: c.item.Alias,
		Method: c.action, Item: c.item.Name, ArgsBytes: int64(len(c.args))}
	record := func(outcome, class string) {
		st.Outcome, st.ErrorClass, st.TotalUS = outcome, class, time.Since(arrived).Microseconds()
		g.Telemetry.Call(c.p.OrgID, st)
	}
	// c.args is already the canonical form; it is what gets hashed, approved
	// and sent upstream (ADR 0001 stage 7).
	sum := sha256.Sum256(c.args)
	digest := hex.EncodeToString(sum[:])

	// Arguments the accepted definition does not allow go no further: no
	// decision, no approval request, nothing upstream.
	if r := g.checkArgs(c); r != nil {
		g.recordRefusal(ctx, c, callID, "refused:"+r.class, store.OutcomeRefused)
		record(store.OutcomeRefused, r.class)
		return r
	}
	dec, err := g.decide(ctx, c, digest, callID)
	st.WaitUS = dec.waited.Microseconds()
	st.DecideUS = time.Since(arrived).Microseconds() - st.WaitUS
	if err != nil {
		var r *refusal
		if errors.As(err, &r) {
			record(store.OutcomeRefused, r.class)
		} else {
			record(store.OutcomeError, "internal")
		}
		return err
	}
	// If the call cannot start after an approval was consumed, put it back.
	restore := func() {
		if dec.approvalID != "" {
			if rerr := g.Approval.Restore(context.WithoutCancel(ctx), c.p.OrgID, dec.approvalID); rerr != nil {
				g.Logger.Error("restoring approval", "id", dec.approvalID, "err", rerr)
			}
		}
	}

	payload, err := g.argumentsPayload(ctx, c, callID)
	if err != nil {
		restore()
		record(store.OutcomeError, "internal")
		return err
	}
	start := time.Now()
	intent := store.AuditEvent{ID: ids.New(), CallID: callID, Time: start, Actor: c.p.Name, ActorID: c.p.TokenID, UserID: c.p.UserID, Client: c.p.Client, Server: c.item.Alias,
		Item: c.item.Name, Action: c.action, Decision: dec.label, ApprovedBy: dec.approvedBy, Payload: payload}
	if err := g.Audit.Intent(ctx, c.p.OrgID, intent); err != nil {
		restore()
		g.Logger.Error("audit intent failed; refusing call", "err", err)
		st.AuditUS = time.Since(start).Microseconds()
		record(store.OutcomeError, "audit_unavailable")
		g.Telemetry.Diagnose(c.p.OrgID, store.Diagnostic{Level: store.LevelError, Category: store.CategoryAudit,
			Message: "the audit record could not be written, so calls are refused"})
		return errors.New("audit unavailable; call refused")
	}
	st.AuditUS = time.Since(start).Microseconds()

	outcome := "error"
	var result any
	connecting := time.Now()
	session, err := g.session(ctx, c)
	st.ConnectUS = time.Since(connecting).Microseconds()
	if err == nil {
		if dec.isSource && (c.action == "tools/call" || c.action == "resources/read") {
			if callerKey := flowCallerKey(c.p); callerKey != "" {
				g.flowTracker().RecordSource(string(c.p.OrgID), callerKey, c.p.Client, c.item.Alias, c.item.Name, time.Now())
			}
		}
		calling := time.Now()
		outcome, result, err = g.callUpstream(ctx, c, session, do)
		st.UpstreamUS = time.Since(calling).Microseconds()
	}
	completion := store.AuditEvent{ID: ids.New(), CallID: callID, Time: time.Now(), Actor: c.p.Name, ActorID: c.p.TokenID, UserID: c.p.UserID, Client: c.p.Client, Server: c.item.Alias,
		Item: c.item.Name, Action: c.action, Outcome: outcome, LatencyMS: time.Since(start).Milliseconds()}
	if aerr := g.Audit.Completion(ctx, c.p.OrgID, completion); aerr != nil {
		g.Logger.Error("audit completion not queued", "err", aerr)
	}
	class := errorClass(err)
	if class == "key_refused" {
		g.Telemetry.Diagnose(c.p.OrgID, store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryGrant, Server: c.item.Alias,
			UserID: c.p.UserID, Message: "the server refused a member's own API key"})
	}
	if err == nil {
		st.ResultBytes = resultSize(result)
	} else {
		outcome = store.OutcomeError
	}
	record(statOutcome(outcome), class)
	return g.clientFacing(c, err)
}

// statOutcome maps the audit outcome of a call that ran to a statistic's.
func statOutcome(outcome string) string {
	switch outcome {
	case store.OutcomeOK, store.OutcomeToolError:
		return outcome
	}
	return store.OutcomeError
}

// errorClass is the fixed class of a failed call for its statistic (ADR 0024
// point 2): never the error's text, which can hold an upstream URL.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	var acct *accountError
	var rpc *jsonrpc.Error
	switch {
	case errors.As(err, &acct):
		return acct.class
	case errors.Is(err, grants.ErrKeyRefused):
		return "key_refused"
	case errors.Is(err, grants.ErrNotConnected):
		return "not_connected"
	case errors.Is(err, errCallTimeout):
		return "timeout"
	case errors.Is(err, upstream.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, errMalformedReply), errors.Is(err, errNeedsInput):
		return "malformed"
	case errors.As(err, &rpc) && !sdkLocalCode(rpc.Code):
		return "upstream_error"
	}
	return "transport"
}

// resultSize estimates what a result costs in the model's context (F17):
// the bytes of its text and of its binary data as base64, the way it is
// relayed. It reads lengths only: encoding a large result a second time on
// the request path doubled its cost (review of 2026-11-15, T-06). Structured
// content is counted only when it is already raw JSON; servers send the same
// data as text too.
func resultSize(v any) int64 {
	var n int64
	b64 := func(b []byte) int64 { return int64(base64.StdEncoding.EncodedLen(len(b))) }
	resource := func(r *mcp.ResourceContents) {
		if r != nil {
			n += int64(len(r.Text)) + b64(r.Blob)
		}
	}
	content := func(c mcp.Content) {
		switch c := c.(type) {
		case *mcp.TextContent:
			n += int64(len(c.Text))
		case *mcp.ImageContent:
			n += b64(c.Data)
		case *mcp.AudioContent:
			n += b64(c.Data)
		case *mcp.EmbeddedResource:
			resource(c.Resource)
		case *mcp.ResourceLink:
			n += int64(len(c.URI) + len(c.Name) + len(c.Title) + len(c.Description))
		}
	}
	switch r := v.(type) {
	case *mcp.CallToolResult:
		for _, c := range r.Content {
			content(c)
		}
		if raw, ok := r.StructuredContent.(json.RawMessage); ok {
			n += int64(len(raw))
		}
	case *mcp.GetPromptResult:
		for _, m := range r.Messages {
			if m != nil {
				content(m.Content)
			}
		}
	case *mcp.ReadResourceResult:
		for _, c := range r.Contents {
			resource(c)
		}
	}
	return n
}

// session picks the upstream session for a call: the server's shared one, or,
// for a server where each person uses their own account (ADR 0017), one that
// carries the caller's own grant. It never falls back to someone else's.
func (g *Gateway) session(ctx context.Context, c call) (*mcp.ClientSession, error) {
	if !g.Upstream.PerPerson(c.item.ServerID) {
		return g.Upstream.Session(c.p.OrgID, c.item.ServerID)
	}
	owner, err := g.grantOwner(ctx, c)
	if err != nil {
		return nil, err
	}
	s, err := g.Upstream.PersonSession(ctx, c.p.OrgID, c.item.ServerID, owner)
	if errors.Is(err, grants.ErrNotConnected) {
		return nil, g.connectFirst(c.item.Alias)
	}
	if errors.Is(err, grants.ErrKeyRefused) {
		// The handshake with the person's own key was refused (ADR 0023).
		return nil, g.keyRefused(c.item.Alias)
	}
	return s, err
}

// grantOwner is the member whose own account a call to a per-person server
// runs as: the active member the calling token belongs to, by user id. The
// person a token names is never looked up (F27), so nobody can use another
// member's account by issuing a token that carries their name.
func (g *Gateway) grantOwner(ctx context.Context, c call) (string, error) {
	if c.p.UserID == "" {
		return "", g.noAccount(c.item.Alias)
	}
	u, err := g.Store.UserByID(ctx, c.p.OrgID, c.p.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		return "", g.noAccount(c.item.Alias)
	}
	if err != nil {
		return "", err
	}
	return u.ID, nil
}

// accountError is a call that needs the person to act in the browser first. Its
// text is fixed and safe to show to the AI client and the person.
type accountError struct {
	msg   string
	class string // no_account, not_connected or key_refused (ADR 0024)
}

func (e *accountError) Error() string { return e.msg }

// noAccount answers a token that belongs to no member: it cannot use anyone's
// account. The person makes a token of their own after signing in.
func (g *Gateway) noAccount(alias string) error {
	const why = "%q runs with each person's own account, and this token does not belong to a member of this Privo. "
	if g.BaseURL == "" {
		return &accountError{fmt.Sprintf(why+"Sign in to the Privo admin site and make a token for this client on the Tokens page.", alias), "no_account"}
	}
	link := strings.TrimSuffix(g.BaseURL, "/") + "/admin/tokens"
	return &accountError{fmt.Sprintf(why+"Sign in and make a token for this client: open %s in your browser.", alias, link), "no_account"}
}

func (g *Gateway) connectFirst(alias string) error {
	if g.BaseURL == "" {
		return &accountError{fmt.Sprintf("Connect your %s account (or enter your API key for it) on the Privo admin site first, then try again.", alias), "not_connected"}
	}
	link := strings.TrimSuffix(g.BaseURL, "/") + "/connect/" + url.PathEscape(alias)
	return &accountError{fmt.Sprintf("Connect your %s account first: open %s in your browser, then try again.", alias, link), "not_connected"}
}

// errCallTimeout means the upstream did not answer within Gateway.CallTimeout.
var errCallTimeout = errors.New("the upstream server did not answer in time")

// callUpstream runs do with the call timeout. It reports a timeout only when
// the deadline was the reason the call failed: a caller that went away, or an
// answer that arrived anyway, is not one (R-04).
func (g *Gateway) callUpstream(ctx context.Context, c call, session *mcp.ClientSession,
	do func(context.Context, *mcp.ClientSession) (string, any, error)) (string, any, error) {
	if g.CallTimeout <= 0 {
		outcome, result, err := do(ctx, session)
		if errors.Is(err, grants.ErrKeyRefused) {
			err = g.keyRefused(c.item.Alias)
		}
		return outcome, result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, g.CallTimeout)
	defer cancel()
	outcome, result, err := do(callCtx, session)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w (server %q, limit %s)", errCallTimeout, c.item.Alias, g.CallTimeout)
	}
	if errors.Is(err, grants.ErrKeyRefused) {
		err = g.keyRefused(c.item.Alias)
	}
	return outcome, result, err
}

// keyRefused tells the person that the server refused their own API key
// (ADR 0023) and where to enter a new one.
func (g *Gateway) keyRefused(alias string) error {
	if g.BaseURL == "" {
		return &accountError{fmt.Sprintf("%s refused your API key. Enter a new one on the Privo admin site, then try again.", alias), "key_refused"}
	}
	link := strings.TrimSuffix(g.BaseURL, "/") + "/connect/" + url.PathEscape(alias)
	return &accountError{fmt.Sprintf("%s refused your API key. Enter a new one: open %s in your browser, then try again.", alias, link), "key_refused"}
}

// sdkLocalCode reports the JSON-RPC codes the SDK raises itself (unknown error,
// client closing, server closing, rejected by transport). It wraps them around
// the text of a local failure such as "Post <url>: connection refused", so an
// error with one of these codes is not an answer from the upstream.
func sdkLocalCode(code int64) bool {
	switch code {
	case -32001, -32003, -32004, -32005:
		return true
	}
	return false
}

// clientFacing decides what the caller of a failed call may read. The text of
// a transport error holds the upstream's URL, and URLs often carry the API key
// in the query (R-02, independent review 2026-10-29). Connection errors are for
// administrators (the server list hides them from members), so the caller gets
// a fixed message and the log gets the detail with the credentials removed from
// every URL. The upstream's own JSON-RPC answer passes through (the error
// alone, not the text the SDK wrapped around it), and so do the gateway's own.
func (g *Gateway) clientFacing(c call, err error) error {
	if err == nil {
		return nil
	}
	var rpc *jsonrpc.Error
	if errors.As(err, &rpc) && !sdkLocalCode(rpc.Code) {
		return rpc
	}
	var r *refusal
	if errors.Is(err, errMalformedReply) || errors.Is(err, errNeedsInput) {
		// The text names a field and a position and nothing the upstream sent.
		g.Logger.Warn("upstream sent a reply the gateway does not relay", "server", c.item.Alias, "action", c.action, "err", err)
		return err
	}
	var acct *accountError
	if errors.As(err, &r) || errors.As(err, &acct) || errors.Is(err, upstream.ErrUnavailable) || errors.Is(err, errCallTimeout) {
		return err
	}
	g.Logger.Warn("upstream call failed", "server", c.item.Alias, "action", c.action, "err", upstream.RedactText(err.Error()))
	return fmt.Errorf("the upstream server %q did not answer or failed; an administrator can see details in the server status and the log", c.item.Alias)
}

// argumentsPayload encrypts the arguments when the org records them.
// Format: 4-byte key version, then the sealed arguments.
func (g *Gateway) argumentsPayload(ctx context.Context, c call, callID string) ([]byte, error) {
	rec, err := g.Store.Setting(ctx, c.p.OrgID, SettingRecordArguments, "false")
	if err != nil || rec != "true" || len(c.args) == 0 {
		return nil, err
	}
	version, dek, err := g.Keys.Current(ctx, c.p.OrgID)
	if err != nil {
		return nil, err
	}
	sealed, err := vault.Seal(dek, c.args, AuditBinding(c.p.OrgID, callID, version))
	if err != nil {
		return nil, err
	}
	return append(binary.BigEndian.AppendUint32(nil, version), sealed...), nil
}

// AuditBinding is the vault binding for recorded audit arguments.
func AuditBinding(org store.OrgID, callID string, keyVersion uint32) vault.Binding {
	return vault.Binding{OrgID: string(org), CredentialID: "audit:" + callID, Purpose: "audit.arguments", KeyVersion: keyVersion, RowVersion: 1}
}

func notFound(kind, name string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
		Text: fmt.Sprintf("%s %q is not available: it may have been removed, blocked or still be awaiting review. Refresh the list and try again.", kind, name),
	}}}
}

func (g *Gateway) callTool(ctx context.Context, p auth.Principal, params *mcp.CallToolParamsRaw) (mcp.Result, error) {
	it, ok, err := g.find(ctx, p, upstream.KindTool, params.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		go g.recordHidden(context.WithoutCancel(ctx), p, upstream.KindTool, "tools/call", func(it upstream.Item) bool { return PublicName(it.Alias, it.Name) == params.Name })
		return notFound("Tool", params.Name), nil
	}
	var result *mcp.CallToolResult
	args, err := Canonical(params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // tool-level error for the model
	}
	err = g.run(ctx, call{p: p, item: it, action: "tools/call", args: args}, func(ctx context.Context, s *mcp.ClientSession) (string, any, error) {
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: it.Name, Arguments: args})
		if err != nil {
			return "error", nil, err
		}
		if err = checkTool(res); err != nil {
			return "error", nil, err
		}
		cleanForRelay(res)
		result = res
		if res.IsError {
			return "tool_error", res, nil
		}
		return "ok", res, nil
	})
	if err != nil {
		// MCP reports tool failures as an error result the model can read,
		// not as a protocol error.
		text := "call failed: " + err.Error()
		var r *refusal
		var acct *accountError
		switch {
		case errors.As(err, &r):
			text = r.msg
		case errors.As(err, &acct):
			text = acct.msg
		}
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil //nolint:nilerr // see comment above
	}
	return result, nil
}

func (g *Gateway) getPrompt(ctx context.Context, p auth.Principal, params *mcp.GetPromptParams) (mcp.Result, error) {
	it, ok, err := g.find(ctx, p, upstream.KindPrompt, params.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		go g.recordHidden(context.WithoutCancel(ctx), p, upstream.KindPrompt, "prompts/get", func(it upstream.Item) bool { return PublicName(it.Alias, it.Name) == params.Name })
		return nil, invalidParams("prompt %q is not available", params.Name)
	}
	args, err := json.Marshal(params.Arguments) // map[string]string: keys sorted by encoding/json
	if err != nil {
		return nil, err
	}
	var result *mcp.GetPromptResult
	err = g.run(ctx, call{p: p, item: it, action: "prompts/get", args: args}, func(ctx context.Context, s *mcp.ClientSession) (string, any, error) {
		res, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: it.Name, Arguments: params.Arguments})
		if err != nil {
			return "error", nil, err
		}
		if err = checkPrompt(res); err != nil {
			return "error", nil, err
		}
		cleanForRelay(res)
		result = res
		return "ok", res, nil
	})
	return result, err
}

// rewriteRead gives every content of an upstream's resources/read reply its
// public URI. The reply is untrusted: no result, no contents, or a null among
// the contents is refused as malformed before anything is changed, because a
// panic here would end the process for every tenant.
func rewriteRead(alias string, res *mcp.ReadResourceResult) error {
	if err := checkRead(res); err != nil {
		return err
	}
	for _, c := range res.Contents {
		c.URI = ResourceURI(alias, c.URI)
	}
	return nil
}

func (g *Gateway) readResource(ctx context.Context, p auth.Principal, params *mcp.ReadResourceParams) (mcp.Result, error) {
	alias, up, ok := ParseResourceURI(params.URI)
	if !ok {
		return nil, invalidParams("resource %q is not a Privo resource URI", params.URI)
	}
	items, err := g.visible(ctx, p, upstream.KindResource)
	if err != nil {
		return nil, err
	}
	idx := slices.IndexFunc(items, func(c upstream.Item) bool { return c.Alias == alias && c.Name == up })
	if idx < 0 {
		go g.recordHidden(context.WithoutCancel(ctx), p, upstream.KindResource, "resources/read", func(it upstream.Item) bool { return it.Alias == alias && it.Name == up })
		return nil, resourceNotFound("resource %q is not available", params.URI)
	}
	it := items[idx]
	var result *mcp.ReadResourceResult
	err = g.run(ctx, call{p: p, item: it, action: "resources/read"}, func(ctx context.Context, s *mcp.ClientSession) (string, any, error) {
		res, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: up})
		if err != nil {
			return "error", nil, err
		}
		if err = rewriteRead(alias, res); err != nil {
			return "error", nil, err
		}
		cleanForRelay(res)
		res.Cacheable = readCache()
		result = res
		return "ok", res, nil
	})
	return result, err
}

// hiddenWindow is how often one caller's calls on one server for one reason
// are recorded when their teams or toolset keep it from them.
const hiddenWindow = time.Minute

// recordHidden audits a call on something that exists and was accepted but
// that the caller may not use. It runs after the answer, on its own, so the
// answer's timing does not tell a hidden name from an unknown one (security
// review of the findings batch). Below: what it records.
//
// It audits a call on something that exists and was accepted but
// that the caller's teams, or their toolset, keep from them (F26 R-03). The
// answer stays that of an unknown name; the record says why, at most once a
// minute per caller, server and reason. An unknown or unreviewed name is not
// recorded: it would let anyone fill the audit.
func (g *Gateway) recordHidden(ctx context.Context, p auth.Principal, kind, action string, match func(upstream.Item) bool) {
	acc, err := g.access(ctx, p)
	if err != nil {
		return
	}
	for _, it := range g.Upstream.Items(p.OrgID) {
		if it.Kind != kind || !match(it) {
			continue
		}
		reason := ""
		switch {
		case !acc.Reaches(it.ServerID):
			reason = "denied:not_in_team"
		case it.Kind == upstream.KindTool && !acc.SeesTool(it.ServerID, it.Name):
			reason = "denied:not_in_toolset"
		default:
			continue
		}
		st, err := g.Store.PinStatuses(ctx, p.OrgID, it.ServerID)
		if err != nil || st[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] != store.PinAccepted {
			continue
		}
		who := p.UserID
		if who == "" {
			who = p.TokenID
		}
		if !g.firstInWindow(string(p.OrgID) + "\x00" + who + "\x00" + it.ServerID + "\x00" + reason) {
			return
		}
		g.recordRefusal(ctx, call{p: p, item: it, action: action}, ids.New(), reason, "refused")
		return
	}
}

// firstInWindow reports whether key was not seen in the last hiddenWindow,
// and remembers it.
func (g *Gateway) firstInWindow(key string) bool {
	now := time.Now()
	g.hiddenMu.Lock()
	defer g.hiddenMu.Unlock()
	if g.hidden == nil {
		g.hidden = map[string]time.Time{}
	}
	if last, ok := g.hidden[key]; ok && now.Sub(last) < hiddenWindow {
		return false
	}
	if len(g.hidden) >= 10000 {
		for k, t := range g.hidden {
			if now.Sub(t) >= hiddenWindow {
				delete(g.hidden, k)
			}
		}
	}
	g.hidden[key] = now
	return true
}
