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
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/buildinfo"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/safe"
	"privo.club/privo/internal/store"
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

// Store is the store subset the gateway uses.
type Store interface {
	PinStatuses(ctx context.Context, org store.OrgID, serverID string) (map[store.PinKey]string, error)
	Setting(ctx context.Context, org store.OrgID, key, def string) (string, error)
	ListPolicies(ctx context.Context, org store.OrgID) ([]store.Policy, error)
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
	// CallTimeout is how long one call to an upstream server may take; zero
	// means no limit (R-04). It starts once the call is allowed and approved.
	CallTimeout time.Duration
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
// teams. The caller's member is the signed-in person, or the person whose own
// token it is (F07 R-01); any other token is in "everyone" only, whatever
// person name it carries. A removed member reaches nothing. Any error is
// returned, so callers fail closed. The admin API uses it too, so both answer
// the same.
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

// CallerRoles are the roles rules see (ADR 0021): a member's current roles,
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
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

// decide runs the decision stage: rules for tool calls, then an approval when
// a rule asks for one. It returns the label written to the audit log.
// decision is the outcome of the decision stage.
type decision struct {
	label      string // written to the audit log: allow or approved:<id>
	approvalID string // set when an approval was consumed
	approvedBy string
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
	var ann *mcp.ToolAnnotations
	if c.item.Tool != nil {
		ann = c.item.Tool.Annotations
	}
	// Rules match tools, prompts and resources alike by upstream name, so a
	// server-wide deny or a default deny covers everything the server offers.
	dec := policy.Evaluate(rules, policy.Request{Roles: CallerRoles(c.p, acc), TokenID: c.p.TokenID, UserID: c.p.UserID, Teams: acc.Teams, Server: c.item.Alias, Tool: c.item.Name,
		PinHash: c.item.Hash, Annotations: ann}, def, time.Now())
	switch dec.Action {
	case store.ActionAllow:
		return decision{label: store.ActionAllow}, nil
	case store.ActionDeny:
		g.recordRefusal(ctx, c, callID, store.ActionDeny, "denied:"+dec.Reason)
		// The model gets no rule details: they would help it probe the policy.
		return decision{}, &refusal{msg: "Denied by your organisation's Privo policy."}
	}
	if c.action != "tools/call" {
		// Approvals cover tool calls; for prompts and resources "ask" fails closed.
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "denied:approval not supported for "+c.action)
		return decision{}, &refusal{msg: "Denied by your organisation's Privo policy."}
	}
	if g.Approval == nil {
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "approvals_unavailable")
		return decision{}, &refusal{msg: "This call needs approval, but approvals are not configured."}
	}
	out, err := g.Approval.Gate(ctx, approval.Request{Org: c.p.OrgID, RequesterID: c.p.TokenID, RequesterName: c.p.Name,
		RequesterPerson: c.p.Person, ServerID: c.item.ServerID, ServerAlias: c.item.Alias, Tool: c.item.Name, ArgsDigest: digest,
		Args: c.args, Reason: dec.Reason, RuleID: dec.RuleID, PinHash: c.item.Hash})
	if errors.Is(err, approval.ErrTooManyPending) {
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "too_many_pending")
		return decision{}, &refusal{msg: "Too many of your calls are already waiting for approval. Wait for decisions before asking again."}
	}
	if err != nil {
		return decision{}, err
	}
	switch out.Result {
	case approval.Approved:
		return decision{label: "approved:" + out.ID, approvalID: out.ID, approvedBy: out.DecidedBy}, nil
	case approval.Withdrawn:
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "withdrawn_by_requester")
		return decision{}, &refusal{msg: "This request was withdrawn in Privo. Call again to ask for approval anew."}
	case approval.Denied:
		g.recordRefusal(ctx, c, callID, store.ActionAsk, "denied_by_approver")
		return decision{}, &refusal{msg: "A person denied this exact call. Do not retry it unchanged."}
	}
	g.recordRefusal(ctx, c, callID, store.ActionAsk, "pending_approval")
	return decision{}, &refusal{msg: fmt.Sprintf("Approval required: request %s is waiting for a person in Privo (%s/approvals/%s). "+
		"Tell the user, then retry exactly the same call with the same arguments once it is approved. It expires at %s.",
		out.ID, g.BaseURL, out.ID, out.ExpiresAt.UTC().Format("15:04 UTC"))}
}

// recordRefusal writes an intent and its completion for a call that did not run.
func (g *Gateway) recordRefusal(ctx context.Context, c call, callID, decision, outcome string) {
	now := time.Now()
	ev := store.AuditEvent{ID: ids.New(), CallID: callID, Time: now, Actor: c.p.Name, ActorID: c.p.TokenID, Server: c.item.Alias,
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

// run executes the pipeline stages around do and records the outcome.
func (g *Gateway) run(ctx context.Context, c call, do func(context.Context, *mcp.ClientSession) (outcome string, err error)) error {
	callID := ids.New()
	// c.args is already the canonical form; it is what gets hashed, approved
	// and sent upstream (ADR 0001 stage 7).
	sum := sha256.Sum256(c.args)
	digest := hex.EncodeToString(sum[:])

	dec, err := g.decide(ctx, c, digest, callID)
	if err != nil {
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
		return err
	}
	start := time.Now()
	intent := store.AuditEvent{ID: ids.New(), CallID: callID, Time: start, Actor: c.p.Name, ActorID: c.p.TokenID, Server: c.item.Alias,
		Item: c.item.Name, Action: c.action, Decision: dec.label, ApprovedBy: dec.approvedBy, Account: g.account(c), Payload: payload}
	if err := g.Audit.Intent(ctx, c.p.OrgID, intent); err != nil {
		restore()
		g.Logger.Error("audit intent failed; refusing call", "err", err)
		return errors.New("audit unavailable; call refused")
	}

	outcome := "error"
	session, err := g.session(ctx, c)
	if err == nil {
		outcome, err = g.callUpstream(ctx, c, session, do)
	}
	completion := store.AuditEvent{ID: ids.New(), CallID: callID, Time: time.Now(), Actor: c.p.Name, ActorID: c.p.TokenID, Server: c.item.Alias,
		Item: c.item.Name, Action: c.action, Outcome: outcome, LatencyMS: time.Since(start).Milliseconds()}
	if aerr := g.Audit.Completion(ctx, c.p.OrgID, completion); aerr != nil {
		g.Logger.Error("audit completion not queued", "err", aerr)
	}
	return g.clientFacing(c, err)
}

// session picks the upstream session for a call: the server's shared one, or,
// for a server where each person uses their own account (ADR 0017), one that
// carries the caller's own grant. It never falls back to someone else's.
func (g *Gateway) session(ctx context.Context, c call) (*mcp.ClientSession, error) {
	if !g.Upstream.PerPerson(c.item.ServerID) {
		return g.Upstream.Session(c.p.OrgID, c.item.ServerID)
	}
	if c.p.UserID == "" {
		return nil, noAccount(c.item.Alias)
	}
	u, err := g.Store.UserByID(ctx, c.p.OrgID, c.p.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		return nil, noAccount(c.item.Alias)
	}
	if err != nil {
		return nil, err
	}
	s, err := g.Upstream.PersonSession(ctx, c.p.OrgID, c.item.ServerID, u.ID)
	if errors.Is(err, grants.ErrNotConnected) {
		return nil, g.connectFirst(c.item.Alias)
	}
	return s, err
}

// account names whose upstream account a call uses (F07 R-01): on a server
// where each person uses their own, the caller's member, the only one session
// ever picks. Elsewhere the server's shared connection is used and it is "".
func (g *Gateway) account(c call) string {
	if c.p.UserID == "" || g.Upstream == nil || !g.Upstream.PerPerson(c.item.ServerID) {
		return ""
	}
	return c.p.Person + " (user " + c.p.UserID + ")"
}

// accountError is a call that needs the person to act in the browser first. Its
// text is fixed and safe to show to the AI client and the person.
type accountError struct{ msg string }

func (e *accountError) Error() string { return e.msg }

func noAccount(alias string) error {
	return &accountError{fmt.Sprintf("%q runs with each person's own account, and this token is not a person's own token. "+
		"Sign in to the Privo admin site and make your token on the Tokens page; if you have no account, ask an administrator for an invite.", alias)}
}

func (g *Gateway) connectFirst(alias string) error {
	if g.BaseURL == "" {
		return &accountError{fmt.Sprintf("Connect your %s account on the Privo admin site first, then try again.", alias)}
	}
	link := strings.TrimSuffix(g.BaseURL, "/") + "/connect/" + url.PathEscape(alias)
	return &accountError{fmt.Sprintf("Connect your %s account first: open %s in your browser, then try again.", alias, link)}
}

// errCallTimeout means the upstream did not answer within Gateway.CallTimeout.
var errCallTimeout = errors.New("the upstream server did not answer in time")

// callUpstream runs do with the call timeout. It reports a timeout only when
// the deadline was the reason the call failed: a caller that went away, or an
// answer that arrived anyway, is not one (R-04).
func (g *Gateway) callUpstream(ctx context.Context, c call, session *mcp.ClientSession,
	do func(context.Context, *mcp.ClientSession) (string, error)) (string, error) {
	if g.CallTimeout <= 0 {
		return do(ctx, session)
	}
	callCtx, cancel := context.WithTimeout(ctx, g.CallTimeout)
	defer cancel()
	outcome, err := do(callCtx, session)
	if err != nil && ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w (server %q, limit %s)", errCallTimeout, c.item.Alias, g.CallTimeout)
	}
	return outcome, err
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
		return notFound("Tool", params.Name), nil
	}
	var result *mcp.CallToolResult
	args, err := Canonical(params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // tool-level error for the model
	}
	err = g.run(ctx, call{p: p, item: it, action: "tools/call", args: args}, func(ctx context.Context, s *mcp.ClientSession) (string, error) {
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: it.Name, Arguments: args})
		if err != nil {
			return "error", err
		}
		if err = checkTool(res); err != nil {
			return "error", err
		}
		cleanForRelay(res)
		result = res
		if res.IsError {
			return "tool_error", nil
		}
		return "ok", nil
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
		return nil, invalidParams("prompt %q is not available", params.Name)
	}
	args, err := json.Marshal(params.Arguments) // map[string]string: keys sorted by encoding/json
	if err != nil {
		return nil, err
	}
	var result *mcp.GetPromptResult
	err = g.run(ctx, call{p: p, item: it, action: "prompts/get", args: args}, func(ctx context.Context, s *mcp.ClientSession) (string, error) {
		res, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: it.Name, Arguments: params.Arguments})
		if err != nil {
			return "error", err
		}
		if err = checkPrompt(res); err != nil {
			return "error", err
		}
		cleanForRelay(res)
		result = res
		return "ok", nil
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
		return nil, resourceNotFound("resource %q is not available", params.URI)
	}
	it := items[idx]
	var result *mcp.ReadResourceResult
	err = g.run(ctx, call{p: p, item: it, action: "resources/read"}, func(ctx context.Context, s *mcp.ClientSession) (string, error) {
		res, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: up})
		if err != nil {
			return "error", err
		}
		if err = rewriteRead(alias, res); err != nil {
			return "error", err
		}
		cleanForRelay(res)
		res.Cacheable = readCache()
		result = res
		return "ok", nil
	})
	return result, err
}
