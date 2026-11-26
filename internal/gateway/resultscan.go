package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/scan"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

const (
	// SettingResultScan controls how the gateway handles Warn findings from
	// scanning upstream results and resource contents ("flag" default, or "block").
	SettingResultScan = "policy.result_scan"

	// Supported modes for SettingResultScan.
	ResultScanFlag  = "flag"
	ResultScanBlock = "block"
)

const (
	resultWithheldMsg       = "Result withheld by Privo: suspicious instructions detected"
	resultSchemaWithheldMsg = "Result withheld by Privo: redacted structured output failed schema validation"
	resultRedactionFailMsg  = "Result withheld by Privo: result could not be checked for secrets"
)

const (
	minRedactedSecretLen = 8
	maxStructuredDepth   = 128
	maxScanAuditFindings = 16
)

// collectCallSecrets gathers active secrets ONLY for the current call's org,
// server and (for per-person servers) verified caller user ID. It never reads
// another org's, another server's or another member's credentials.
func (g *Gateway) collectCallSecrets(ctx context.Context, c call, owner string, preSecrets []string) ([]string, error) {
	var srv store.Server
	var hasSrv bool
	if g.Upstream != nil {
		srv, hasSrv = g.Upstream.Server(c.p.OrgID, c.item.ServerID)
	}
	perPerson := (hasSrv && srv.Config.PerPerson()) || (g.Upstream != nil && g.Upstream.PerPerson(c.item.ServerID))

	if !hasSrv && g.Keys != nil && g.Store != nil {
		s, err := g.Store.ServerByID(ctx, c.p.OrgID, c.item.ServerID)
		switch {
		case err == nil:
			srv, hasSrv = s, true
			perPerson = s.Config.PerPerson()
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}

	if perPerson {
		return g.collectPersonSecrets(ctx, c, owner, preSecrets)
	}
	return g.collectSharedSecrets(ctx, c, srv, hasSrv)
}

func (g *Gateway) collectPersonSecrets(ctx context.Context, c call, owner string, preSecrets []string) ([]string, error) {
	secrets := append([]string(nil), preSecrets...)
	if owner == "" || g.Keys == nil || g.Store == nil {
		return secrets, nil
	}
	gr, err := g.Store.Grant(ctx, c.p.OrgID, c.item.ServerID, owner)
	if errors.Is(err, store.ErrNotFound) {
		return secrets, nil
	}
	if err != nil {
		return nil, err
	}
	dek, err := g.Keys.DEK(ctx, c.p.OrgID, gr.Credential.KeyVersion)
	if err != nil {
		return nil, err
	}
	pt, err := vault.Open(dek, gr.Credential.Ciphertext, vault.Binding{
		OrgID:        string(c.p.OrgID),
		CredentialID: gr.Credential.ID,
		OwnerID:      gr.Credential.OwnerID,
		ServerID:     gr.Credential.ServerID,
		Purpose:      gr.Credential.Purpose,
		KeyVersion:   gr.Credential.KeyVersion,
		RowVersion:   gr.Credential.RowVersion,
	})
	if err != nil {
		return nil, err
	}
	var tok upstreamauth.Token
	if err := json.Unmarshal(pt, &tok); err != nil {
		return nil, errors.New("unreadable grant payload")
	}
	if tok.AccessToken != "" {
		secrets = append(secrets, tok.AccessToken)
	}
	if tok.RefreshToken != "" {
		secrets = append(secrets, tok.RefreshToken)
	}
	return secrets, nil
}

func (g *Gateway) collectSharedSecrets(ctx context.Context, c call, srv store.Server, hasSrv bool) ([]string, error) {
	if g.Keys == nil || g.Store == nil {
		return nil, nil
	}
	creds, err := g.Store.ServerCredentials(ctx, c.p.OrgID, c.item.ServerID)
	if err != nil {
		return nil, err
	}
	var secrets []string
	for _, cred := range creds {
		if cred.OwnerID != "" {
			continue
		}
		kind, name, found := strings.Cut(cred.Purpose, ":")
		if !found || (kind != "header" && kind != "env") {
			continue
		}
		if hasSrv {
			if kind == "header" && !slices.Contains(srv.Config.HeaderNames, name) {
				continue
			}
			if kind == "env" && !slices.Contains(srv.Config.EnvNames, name) {
				continue
			}
		}
		dek, err := g.Keys.DEK(ctx, c.p.OrgID, cred.KeyVersion)
		if err != nil {
			return nil, err
		}
		pt, err := vault.Open(dek, cred.Ciphertext, vault.Binding{
			OrgID:        string(c.p.OrgID),
			CredentialID: cred.ID,
			OwnerID:      cred.OwnerID,
			ServerID:     c.item.ServerID,
			Purpose:      cred.Purpose,
			KeyVersion:   cred.KeyVersion,
			RowVersion:   cred.RowVersion,
		})
		if err != nil {
			return nil, err
		}
		val := string(pt)
		secrets = append(secrets, val)
		// Shared Authorization headers commonly carry "Bearer <token>"; redact
		// the bare token too in case an upstream echoes it without the scheme.
		if kind == "header" && len(val) > len("bearer ") && strings.EqualFold(val[:len("bearer ")], "bearer ") {
			if bare := strings.TrimSpace(val[len("bearer "):]); bare != "" {
				secrets = append(secrets, bare)
			}
		}
	}
	return secrets, nil
}

// secretReplacer builds a single-pass longest-match-first replacer for all
// secrets of at least minRedactedSecretLen bytes and their encoded variants.
func secretReplacer(secrets []string) *strings.Replacer {
	seen := map[string]struct{}{}
	add := func(v string) {
		if len(v) >= minRedactedSecretLen {
			seen[v] = struct{}{}
		}
	}
	for _, s := range secrets {
		if len(s) < minRedactedSecretLen {
			continue
		}
		add(s)
		add(url.QueryEscape(s))
		add(base64.StdEncoding.EncodeToString([]byte(s)))
		add(base64.RawURLEncoding.EncodeToString([]byte(s)))
	}
	if len(seen) == 0 {
		return nil
	}
	variants := make([]string, 0, len(seen))
	for v := range seen {
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		if len(variants[i]) != len(variants[j]) {
			return len(variants[i]) > len(variants[j])
		}
		return variants[i] < variants[j]
	})
	pairs := make([]string, 0, len(variants)*2)
	for _, v := range variants {
		pairs = append(pairs, v, "[REDACTED]")
	}
	return strings.NewReplacer(pairs...)
}

func redactResult(rpl *strings.Replacer, res any) (bool, error) {
	if rpl == nil || res == nil {
		return false, nil
	}
	structuredChanged := false
	redactResource := func(rc *mcp.ResourceContents) {
		if rc == nil {
			return
		}
		rc.Text = rpl.Replace(rc.Text)
		rc.URI = rpl.Replace(rc.URI)
	}
	redactContent := func(c mcp.Content) {
		switch v := c.(type) {
		case *mcp.TextContent:
			if v != nil {
				v.Text = rpl.Replace(v.Text)
			}
		case *mcp.EmbeddedResource:
			if v != nil {
				redactResource(v.Resource)
			}
		case *mcp.ResourceLink:
			if v != nil {
				v.URI = rpl.Replace(v.URI)
				v.Name = rpl.Replace(v.Name)
				v.Title = rpl.Replace(v.Title)
				v.Description = rpl.Replace(v.Description)
			}
		}
	}
	switch r := res.(type) {
	case *mcp.CallToolResult:
		if r == nil {
			return false, nil
		}
		for _, c := range r.Content {
			redactContent(c)
		}
		if r.StructuredContent != nil {
			sc, changed, err := redactStructuredValue(rpl, r.StructuredContent, 0)
			if err != nil {
				return false, err
			}
			if changed {
				r.StructuredContent = sc
				structuredChanged = true
			}
		}
	case *mcp.GetPromptResult:
		if r == nil {
			return false, nil
		}
		r.Description = rpl.Replace(r.Description)
		for _, m := range r.Messages {
			if m != nil {
				redactContent(m.Content)
			}
		}
	case *mcp.ReadResourceResult:
		if r == nil {
			return false, nil
		}
		for _, rc := range r.Contents {
			redactResource(rc)
		}
	}
	return structuredChanged, nil
}

func redactStructuredValue(rpl *strings.Replacer, v any, depth int) (any, bool, error) {
	if v == nil {
		return nil, false, nil
	}
	if depth > maxStructuredDepth {
		return nil, false, errors.New("structured content exceeds maximum nesting depth")
	}
	switch t := v.(type) {
	case string:
		out := rpl.Replace(t)
		return out, out != t, nil
	case json.RawMessage:
		dec := json.NewDecoder(bytes.NewReader(t))
		dec.UseNumber()
		var parsed any
		if err := dec.Decode(&parsed); err != nil {
			return nil, false, err
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, false, errors.New("unexpected trailing JSON data")
		}
		nv, changed, err := redactStructuredValue(rpl, parsed, depth+1)
		if err != nil || !changed {
			return t, false, err
		}
		b, err := json.Marshal(nv)
		if err != nil {
			return nil, false, err
		}
		return json.RawMessage(b), true, nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		changed := false
		for _, k := range keys {
			rk := rpl.Replace(k)
			rv, vc, err := redactStructuredValue(rpl, t[k], depth+1)
			if err != nil {
				return nil, false, err
			}
			if rk != k || vc {
				changed = true
			}
			out[rk] = rv
		}
		if !changed {
			return t, false, nil
		}
		return out, true, nil
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]string, len(t))
		changed := false
		for _, k := range keys {
			rk := rpl.Replace(k)
			rv := rpl.Replace(t[k])
			if rk != k || rv != t[k] {
				changed = true
			}
			out[rk] = rv
		}
		if !changed {
			return t, false, nil
		}
		return out, true, nil
	case []any:
		var out []any
		changed := false
		for i, e := range t {
			re, ec, err := redactStructuredValue(rpl, e, depth+1)
			if err != nil {
				return nil, false, err
			}
			if ec {
				if !changed {
					out = make([]any, len(t))
					copy(out, t)
					changed = true
				}
				out[i] = re
			}
		}
		if !changed {
			return t, false, nil
		}
		return out, true, nil
	case []string:
		var out []string
		changed := false
		for i, e := range t {
			re := rpl.Replace(e)
			if re != e {
				if !changed {
					out = make([]string, len(t))
					copy(out, t)
					changed = true
				}
				out[i] = re
			}
		}
		if !changed {
			return t, false, nil
		}
		return out, true, nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return v, false, nil
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, false, err
		}
		nv, changed, err := redactStructuredValue(rpl, json.RawMessage(raw), depth+1)
		if err != nil || !changed {
			return v, false, err
		}
		return nv, true, nil
	}
}

func (g *Gateway) checkOutputSchema(c call, res any) error {
	r, ok := res.(*mcp.CallToolResult)
	if !ok || r == nil || c.item.Tool == nil || c.item.Tool.OutputSchema == nil {
		return nil
	}
	var raw json.RawMessage
	if rm, ok := r.StructuredContent.(json.RawMessage); ok {
		raw = rm
	} else {
		var err error
		raw, err = json.Marshal(r.StructuredContent)
		if err != nil {
			return err
		}
	}
	return g.outputs.Check(c.item.Hash, c.item.Tool.OutputSchema, raw)
}

// processResult redacts active call secrets, re-validates modified
// StructuredContent against OutputSchema, and scans the result for hidden
// instructions according to the org's policy.result_scan setting.
// It returns the completion audit outcome, any refusal error, and whether the
// call succeeded normally (for CallStat) despite being flagged.
func (g *Gateway) processResult(ctx context.Context, c call, callID string, rpl *strings.Replacer, baseOutcome string, result any) (string, error) {
	completionOutcome := baseOutcome
	structuredChanged, rerr := redactResult(rpl, result)
	if rerr != nil {
		g.recordScanAudit(ctx, c, callID, "result.blocked:redaction", "block", []scan.ResultFinding{{Rule: "redaction", Severity: scan.Warn, Offset: 0}})
		return "result.blocked:redaction", &refusal{msg: resultRedactionFailMsg, class: "result_blocked"}
	}
	if structuredChanged {
		if serr := g.checkOutputSchema(c, result); serr != nil {
			g.recordSchemaBlockedAudit(ctx, c, callID)
			return "result.blocked:output_schema", &refusal{msg: resultSchemaWithheldMsg, class: "result_blocked"}
		}
	}

	warns := uniqueWarnFindings(scan.Result(result, scan.DefaultResultCap))
	if len(warns) == 0 {
		return completionOutcome, nil
	}
	mode, serr := g.Store.Setting(ctx, c.p.OrgID, SettingResultScan, ResultScanFlag)
	// Fail closed: if the setting cannot be read or holds an unknown value, withhold.
	if serr != nil || mode != ResultScanFlag {
		g.recordScanAudit(ctx, c, callID, "result.blocked:", "block", warns)
		return "result.blocked:" + warns[0].Rule, &refusal{msg: resultWithheldMsg, class: "result_blocked"}
	}
	g.recordScanAudit(ctx, c, callID, "result.flagged:", "flag", warns)
	return "result.flagged:" + warns[0].Rule, nil
}

func uniqueWarnFindings(findings []scan.ResultFinding) []scan.ResultFinding {
	seen := map[string]bool{}
	var warns []scan.ResultFinding
	for _, f := range findings {
		if f.Severity != scan.Warn || seen[f.Rule] {
			continue
		}
		seen[f.Rule] = true
		warns = append(warns, f)
		if len(warns) >= maxScanAuditFindings {
			break
		}
	}
	return warns
}

func (g *Gateway) recordScanAudit(ctx context.Context, c call, callID, prefix, decision string, warns []scan.ResultFinding) {
	now := time.Now()
	for _, f := range warns {
		action := prefix + f.Rule
		if g.Logger != nil {
			g.Logger.Warn("result scan finding", "server", c.item.Alias, "action", c.action, "rule", f.Rule, "offset", f.Offset, "decision", decision)
		}
		if g.Audit == nil {
			continue
		}
		ev := store.AuditEvent{
			ID:       ids.New(),
			CallID:   callID,
			Time:     now,
			Actor:    c.p.Name,
			ActorID:  c.p.TokenID,
			UserID:   c.p.UserID,
			Client:   c.p.Client,
			Server:   c.item.Alias,
			Item:     c.item.Name,
			Action:   action,
			Decision: decision,
			Outcome:  fmt.Sprintf("offset:%d", f.Offset),
		}
		if err := g.Audit.Admin(context.WithoutCancel(ctx), c.p.OrgID, ev); err != nil && g.Logger != nil {
			g.Logger.Error("audit result scan event", "action", action, "err", err)
		}
	}
}

func (g *Gateway) recordSchemaBlockedAudit(ctx context.Context, c call, callID string) {
	if g.Logger != nil {
		g.Logger.Warn("redacted output failed schema validation", "server", c.item.Alias, "action", c.action, "item", c.item.Name)
	}
	if g.Audit == nil {
		return
	}
	ev := store.AuditEvent{
		ID:       ids.New(),
		CallID:   callID,
		Time:     time.Now(),
		Actor:    c.p.Name,
		ActorID:  c.p.TokenID,
		UserID:   c.p.UserID,
		Client:   c.p.Client,
		Server:   c.item.Alias,
		Item:     c.item.Name,
		Action:   "result.blocked:output_schema",
		Decision: "block",
		Outcome:  "invalid_output_schema",
	}
	if err := g.Audit.Admin(context.WithoutCancel(ctx), c.p.OrgID, ev); err != nil && g.Logger != nil {
		g.Logger.Error("audit output schema event", "err", err)
	}
}
