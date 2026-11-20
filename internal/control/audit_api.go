package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// The audit log for those who read it (F12, ADR 0006, 0028): search, export,
// the integrity of the chain, checkpoints to keep outside Privo, erasure.

// auditQuery reads the filters of a search from the query string.
func auditQuery(r *http.Request) (store.AuditQuery, error) {
	q := r.URL.Query()
	aq := store.AuditQuery{Kind: q.Get("kind"), Actor: q.Get("actor"), Server: q.Get("server"), Action: q.Get("action"),
		Decision: q.Get("decision"), Before: q.Get("before")}
	aq.Limit, _ = strconv.Atoi(q.Get("limit"))
	for name, dst := range map[string]*time.Time{"from": &aq.From, "to": &aq.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return aq, errorf(http.StatusBadRequest, "%s must be a time like 2026-11-20T09:00:00Z", name)
			}
			*dst = t
		}
	}
	for _, v := range []string{aq.Actor, aq.Server, aq.Action, aq.Decision} {
		if len(v) > 200 {
			return aq, errorf(http.StatusBadRequest, "a filter is too long")
		}
	}
	return aq, nil
}

func (a *API) search(r *http.Request, p auth.Principal, aq store.AuditQuery) ([]store.CallRecord, string, error) {
	recs, next, err := a.Store.SearchAudit(r.Context(), p.OrgID, aq)
	if errors.Is(err, store.ErrBadAuditQuery) {
		return nil, "", errorf(http.StatusBadRequest, "kind must be calls or admin, and before the next value of a previous page")
	}
	return recs, next, err
}

// listAudit is a page of calls (or admin events with kind=admin), newest
// first, filtered by actor, server, action prefix, decision prefix and time.
func (a *API) listAudit(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	aq, err := auditQuery(r)
	if err != nil {
		return err
	}
	recs, next, err := a.search(r, p, aq)
	if err != nil {
		return err
	}
	persons, err := a.personsOf(r, p, recs)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, c := range recs {
		out = append(out, a.auditView(r, p, c, aq.Kind == "admin", persons))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "next": next})
	return nil
}

// personsOf maps the user ids the records name to their person names (F27):
// whose account and teams a call ran as, whom an admin event is about.
func (a *API) personsOf(r *http.Request, p auth.Principal, recs []store.CallRecord) (map[string]string, error) {
	persons := map[string]string{}
	if !slices.ContainsFunc(recs, func(c store.CallRecord) bool { return c.Intent.UserID != "" }) {
		return persons, nil
	}
	users, err := a.Store.ListUsers(r.Context(), p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		persons[u.ID] = u.Person
	}
	return persons, nil
}

func (a *API) auditView(r *http.Request, p auth.Principal, c store.CallRecord, admin bool, persons map[string]string) map[string]any {
	i := c.Intent
	if admin {
		e := map[string]any{"id": i.ID, "time": i.Time, "actor": i.Actor, "actor_id": i.ActorID, "action": i.Action, "decision": i.Decision, "outcome": i.Outcome}
		if i.Server != "" {
			e["server"] = i.Server
		}
		if i.Client != "" {
			e["client"] = i.Client
		}
		if i.UserID != "" {
			e["user_id"], e["user"] = i.UserID, persons[i.UserID]
		}
		return e
	}
	e := map[string]any{
		"id": i.CallID, "time": i.Time, "actor": i.Actor, "actor_id": i.ActorID, "server": i.Server,
		"item": i.Item, "action": i.Action, "decision": i.Decision, "outcome": "pending",
	}
	if i.Action == "tools/call" {
		e["tool"] = i.Item
	}
	if i.ApprovedBy != "" {
		e["approved_by"] = i.ApprovedBy
	}
	if i.UserID != "" {
		e["user_id"], e["user"] = i.UserID, persons[i.UserID]
	}
	if i.Client != "" {
		e["client"] = i.Client // the AI client, when it signed in with OAuth (ADR 0022)
	}
	if c.Completion != nil {
		e["outcome"], e["latency_ms"] = c.Completion.Outcome, c.Completion.LatencyMS
	}
	if len(i.Payload) > 4 {
		if args, err := a.openPayload(r.Context(), p.OrgID, i.CallID, i.Payload); err == nil {
			e["arguments"] = args
		} else {
			e["arguments_error"] = "cannot decrypt"
		}
	}
	return e
}

// maxExportRows bounds one CSV export.
const maxExportRows = 10000

// exportAudit returns the matching events as CSV (in {"csv": ...}, so the app
// and scripts read it alike), newest first, at most 10,000 rows. Recorded
// arguments are not in it. The export itself is audited.
func (a *API) exportAudit(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	aq, err := auditQuery(r)
	if err != nil {
		return err
	}
	admin := aq.Kind == "admin"
	var b bytes.Buffer
	cw := csv.NewWriter(&b)
	header := []string{"time", "call", "actor", "actor_id", "client", "user_id", "server", "item", "action", "decision", "approved_by", "outcome", "latency_ms"}
	if admin {
		header = []string{"time", "id", "actor", "actor_id", "client", "user_id", "server", "action", "decision", "outcome"}
	}
	_ = cw.Write(header)
	rows, truncated := 0, false
	aq.Limit = 1000
	for {
		recs, next, err := a.search(r, p, aq)
		if err != nil {
			return err
		}
		for _, c := range recs {
			if rows == maxExportRows {
				truncated = true
				break
			}
			i := c.Intent
			at := i.Time.UTC().Format(time.RFC3339Nano)
			if admin {
				_ = cw.Write(csvSafe([]string{at, i.ID, i.Actor, i.ActorID, i.Client, i.UserID, i.Server, i.Action, i.Decision, i.Outcome}))
			} else {
				outcome, latency := "pending", ""
				if c.Completion != nil {
					outcome, latency = c.Completion.Outcome, strconv.FormatInt(c.Completion.LatencyMS, 10)
				}
				_ = cw.Write(csvSafe([]string{at, i.CallID, i.Actor, i.ActorID, i.Client, i.UserID, i.Server, i.Item, i.Action, i.Decision, i.ApprovedBy, outcome, latency}))
			}
			rows++
		}
		if truncated || next == "" {
			break
		}
		aq.Before = next
	}
	cw.Flush()
	a.adminEvent(r.Context(), p, fmt.Sprintf("audit.export:%d", rows), "")
	writeJSON(w, http.StatusOK, map[string]any{"csv": b.String(), "rows": rows, "truncated": truncated})
	return nil
}

// csvSafe keeps a spreadsheet from reading a cell as a formula: upstream
// names and actors are text from outside.
func csvSafe(cells []string) []string {
	for i, c := range cells {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			cells[i] = "'" + c
		}
	}
	return cells
}

// auditIntegrity verifies the org's chain against the keys derived from the
// master key and says what it found, with the retention setting.
func (a *API) auditIntegrity(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if a.AuditKeys == nil {
		return errorf(http.StatusServiceUnavailable, "the audit chain is not set up on this gateway")
	}
	trusted, err := audit.KeysFrom(a.AuditKeys, p.OrgID)
	if err != nil {
		return err
	}
	rep, err := audit.Verify(r.Context(), a.Store, p.OrgID, trusted)
	if err != nil {
		return err
	}
	days, _ := a.Store.Setting(r.Context(), p.OrgID, audit.SettingRetentionDays, "0")
	n, _ := strconv.Atoi(days)
	a.adminEvent(r.Context(), p, fmt.Sprintf("audit.verify:%t", rep.OK), "")
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "retention_days": n})
	return nil
}

// auditCheckpoints lists signed block heads after the block `after`, at most
// 500, for a customer to keep outside Privo (ADR 0028).
func (a *API) auditCheckpoints(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	blocks, err := a.Store.AuditBlocks(r.Context(), p.OrgID, max(after, 0), 500)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, map[string]any{"seq": b.Seq, "first": b.FirstSeq, "last": b.LastSeq, "prev_hash": b.PrevHash, "hash": b.Hash,
			"key_id": b.KeyID, "signature": b.Signature, "sealed_at": b.SealedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": p.OrgID, "checkpoints": out})
	return nil
}

// auditKeys publishes the public keys that sign this org's blocks.
func (a *API) auditKeys(w http.ResponseWriter, _ *http.Request, p auth.Principal) error {
	if a.AuditKeys == nil {
		return errorf(http.StatusServiceUnavailable, "the audit chain is not set up on this gateway")
	}
	trusted, err := audit.KeysFrom(a.AuditKeys, p.OrgID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for id, pub := range trusted {
		out = append(out, map[string]any{"key_id": id, "algorithm": "Ed25519", "public_key": base64.StdEncoding.EncodeToString(ed25519.PublicKey(pub))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
	return nil
}

// eraseAudit clears the recorded arguments of a person's calls (GDPR):
// those made with any of their tokens or as them, keeping the chain valid.
func (a *API) eraseAudit(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Person string `json:"person"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	u, err := a.Store.UserByPerson(r.Context(), p.OrgID, strings.TrimSpace(req.Person))
	if errors.Is(err, store.ErrNotFound) {
		return errorf(http.StatusNotFound, "no member %q", req.Person)
	}
	if err != nil {
		return err
	}
	actors := []string{"user:" + u.ID}
	toks, err := a.Store.ListAllTokens(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.UserID == u.ID || t.Person == u.Person {
			actors = append(actors, t.ID)
		}
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(),
		Action: "audit.erase:" + u.Person, Decision: "allow", Outcome: "ok"}
	n, err := a.Store.EraseAuditPayloads(r.Context(), p.OrgID, store.Erasure{UserID: u.ID, ActorIDs: actors}, ev)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"person": u.Person, "erased": n})
	return nil
}
