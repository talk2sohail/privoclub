package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/store"
)

// Rules as data (ADR 0024): one checked input for a rule, used by the API's
// create and by import, and the YAML file rules export to and import from.

// ruleInput is a rule as a person writes it, in JSON or YAML.
type ruleInput struct {
	Server     string             `json:"server" yaml:"server"`
	Tool       string             `json:"tool" yaml:"tool"`
	Subject    string             `json:"subject" yaml:"subject"`
	Action     string             `json:"action" yaml:"action"`
	Priority   int                `json:"priority" yaml:"priority,omitempty"`
	Note       string             `json:"note" yaml:"note,omitempty"`
	ExpiresIn  string             `json:"expires_in" yaml:"-"`
	ExpiresAt  *time.Time         `json:"-" yaml:"expires_at,omitempty"`
	PinHash    string             `json:"-" yaml:"pin_hash,omitempty"`
	Conditions []policy.Condition `json:"conditions" yaml:"when,omitempty"`
}

// policy checks the input and builds the rule. The subject is as stored:
// callers map "person:" to "user:" first.
func (in ruleInput) policy(p auth.Principal, now time.Time) (store.Policy, error) {
	if in.Subject == "" {
		in.Subject = "*"
	}
	if in.Tool == "" {
		in.Tool = "*"
	}
	switch {
	case in.Server != "*" && !aliasRe.MatchString(in.Server):
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "server must be a server alias or *")
	case !policy.ValidGlob(in.Tool):
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "tool must be a name or glob pattern")
	case !policy.ValidSubject(in.Subject):
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "subject must be *, role:<role>, team:<slug>, user:<id> or token:<id>")
	case !policy.ValidAction(in.Action):
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "action must be allow, ask or deny")
	case in.Priority < -1000 || in.Priority > 1000:
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "priority must be between -1000 and 1000")
	case len(in.Note) > 500:
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "note is too long")
	case len(in.PinHash) > 128:
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "pin_hash is too long")
	}
	if err := policy.ValidConditions(in.Conditions); err != nil {
		return store.Policy{}, errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	pol := store.Policy{ID: ids.New(), Server: in.Server, Tool: in.Tool, Subject: in.Subject, Action: in.Action, Priority: in.Priority,
		PinHash: in.PinHash, Note: in.Note, CreatedBy: p.Name, CreatedAt: now}
	if len(in.Conditions) > 0 {
		cs := make([]policy.Condition, len(in.Conditions))
		for i, c := range in.Conditions {
			cs[i] = c.Normalize()
		}
		b, err := json.Marshal(cs)
		if err != nil {
			return store.Policy{}, err
		}
		pol.Conditions = string(b)
	}
	switch {
	case in.ExpiresIn != "":
		d, err := time.ParseDuration(in.ExpiresIn)
		if err != nil || d <= 0 || d > 90*24*time.Hour {
			return store.Policy{}, errorf(http.StatusUnprocessableEntity, "expires_in must be a duration up to 2160h")
		}
		exp := now.Add(d)
		pol.ExpiresAt = &exp
	case in.ExpiresAt != nil:
		if !in.ExpiresAt.After(now) {
			return store.Policy{}, errorf(http.StatusUnprocessableEntity, "expires_at is in the past")
		}
		exp := *in.ExpiresAt
		pol.ExpiresAt = &exp
	}
	return pol, nil
}

// rulesFile is the YAML file of an org's rules.
type rulesFile struct {
	Version int         `yaml:"version"`
	Default string      `yaml:"default,omitempty"`
	Rules   []ruleInput `yaml:"rules"`
}

// Limits on an import.
const (
	maxImportBytes = 1 << 20
	maxImportRules = 2000
)

// exportPolicies writes the rules and the default as YAML: grants and
// expired rules are left out; a rule for one member names their person
// ("person:<name>"), so the file reads well and moves between installs.
func (a *API) exportPolicies(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	rules, err := a.Store.ListPolicies(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	users, err := a.Store.ListUsers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	person := map[string]string{}
	for _, u := range users {
		if u.DisabledAt == nil {
			person[u.ID] = u.Person
		}
	}
	def, err := a.Store.Setting(r.Context(), p.OrgID, gateway.SettingPolicyDefault, store.ActionAllow)
	if err != nil {
		return err
	}
	f := rulesFile{Version: 1, Default: def, Rules: []ruleInput{}}
	skipped := 0
	for _, pol := range rules {
		if pol.GrantOf != "" {
			continue
		}
		// A rule for a removed member matches nobody and would not import.
		if id, ok := strings.CutPrefix(pol.Subject, "user:"); ok && person[id] == "" {
			skipped++
			continue
		}
		cs, err := policy.RuleConditions(pol)
		if err != nil {
			return err
		}
		subject := pol.Subject
		if id, ok := strings.CutPrefix(subject, "user:"); ok {
			subject = "person:" + person[id]
		}
		f.Rules = append(f.Rules, ruleInput{Server: pol.Server, Tool: pol.Tool, Subject: subject, Action: pol.Action, Priority: pol.Priority,
			Note: pol.Note, ExpiresAt: pol.ExpiresAt, PinHash: pol.PinHash, Conditions: cs})
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, fmt.Sprintf("policy.export:%d", len(f.Rules)), "")
	writeJSON(w, http.StatusOK, map[string]any{"yaml": b.String(), "rules": len(f.Rules), "skipped": skipped})
	return nil
}

// importPolicies reads a rules file: {"yaml": "...", "mode": "merge"|"replace",
// "dry_run": true}. Every rule is checked first; one bad rule refuses all.
// replace deletes every rule but grants; it all happens in one transaction.
func (a *API) importPolicies(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		YAML   string `json:"yaml"`
		Mode   string `json:"mode"`
		DryRun bool   `json:"dry_run"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Mode != "merge" && req.Mode != "replace" {
		return errorf(http.StatusUnprocessableEntity, "mode must be merge or replace")
	}
	if len(req.YAML) > maxImportBytes {
		return errorf(http.StatusRequestEntityTooLarge, "the file is larger than %d bytes", maxImportBytes)
	}
	var f rulesFile
	dec := yaml.NewDecoder(strings.NewReader(req.YAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return errorf(http.StatusUnprocessableEntity, "the file is not a rules file: %v", err)
	}
	if f.Version != 1 {
		return errorf(http.StatusUnprocessableEntity, "version must be 1")
	}
	if len(f.Rules) > maxImportRules {
		return errorf(http.StatusUnprocessableEntity, "at most %d rules", maxImportRules)
	}
	if f.Default != "" && !policy.ValidAction(f.Default) {
		return errorf(http.StatusUnprocessableEntity, "default must be allow, ask or deny")
	}
	now := time.Now()
	pols := make([]store.Policy, 0, len(f.Rules))
	for i, in := range f.Rules {
		in.ExpiresIn = ""
		if name, ok := strings.CutPrefix(in.Subject, "person:"); ok {
			u, err := a.Store.UserByPerson(r.Context(), p.OrgID, name)
			if err != nil || u.DisabledAt != nil {
				return errorf(http.StatusUnprocessableEntity, "rule %d: there is no member %q", i+1, name)
			}
			in.Subject = "user:" + u.ID
		}
		pol, err := in.policy(p, now)
		if err != nil {
			return errorf(http.StatusUnprocessableEntity, "rule %d: %v", i+1, err)
		}
		pols = append(pols, pol)
	}
	existing, err := a.Store.ListPolicies(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	replaced := 0
	if req.Mode == "replace" {
		for _, pol := range existing {
			if pol.GrantOf == "" {
				replaced++
			}
		}
	}
	resp := map[string]any{"mode": req.Mode, "dry_run": req.DryRun, "add": len(pols), "delete": replaced, "default": f.Default}
	if req.DryRun {
		// Teams and members are checked as the import would check them.
		if err := a.checkImport(r.Context(), p, pols); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: p.Name, ActorID: p.ActorID(),
		Action: fmt.Sprintf("policy.import:%s:+%d:-%d", req.Mode, len(pols), replaced), Decision: "allow", Outcome: "ok"}
	switch err := a.Store.ImportPolicies(r.Context(), p.OrgID, req.Mode == "replace", pols, gateway.SettingPolicyDefault, f.Default, ev); {
	case errors.Is(err, store.ErrUnknownTeam):
		return errorf(http.StatusUnprocessableEntity, "a rule names a team that does not exist; nothing was imported")
	case errors.Is(err, store.ErrUnknownUser):
		return errorf(http.StatusUnprocessableEntity, "a rule names a member who does not exist; nothing was imported")
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// checkImport finds the teams and members that the rules name and the org
// does not have, without writing anything.
func (a *API) checkImport(ctx context.Context, p auth.Principal, pols []store.Policy) error {
	teams, err := a.Store.ListTeams(ctx, p.OrgID)
	if err != nil {
		return err
	}
	known := map[string]bool{store.TeamEveryone: true}
	for _, t := range teams {
		known[t.Slug] = true
	}
	for i, pol := range pols {
		if slug, ok := strings.CutPrefix(pol.Subject, "team:"); ok && !known[slug] {
			return errorf(http.StatusUnprocessableEntity, "rule %d: there is no team %q", i+1, slug)
		}
		if id, ok := strings.CutPrefix(pol.Subject, "user:"); ok {
			if u, err := a.Store.UserByID(ctx, p.OrgID, id); err != nil || u.DisabledAt != nil {
				return errorf(http.StatusUnprocessableEntity, "rule %d: there is no member %q", i+1, id)
			}
		}
	}
	return nil
}
