package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

const (
	// maxArgsShown bounds the arguments printed for one approval; what is
	// longer can only be denied here (an approver must see what they allow).
	maxArgsShown = 4096
	// maxApprovalsShown bounds the page.
	maxApprovalsShown = 50
)

var approvalIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// flashes are the only messages the result parameter can select; anything else
// in the URL is ignored, so a link cannot make the page say something new.
var flashes = map[string]flash{
	"approved": {"ok", "Approved. The call can go ahead."},
	"denied":   {"ok", "Denied."},
	"self":     {"warn", "You cannot approve your own request. Someone else has to decide it."},
	"gone":     {"warn", "That request is no longer waiting: it was decided or it expired."},
}

func (s *Server) denyRole(w http.ResponseWriter, sess store.Session, u store.User) {
	s.pageFor(w, http.StatusForbidden, sess, u, "Not allowed", "Your role cannot decide approvals.")
}

func (s *Server) approvalsPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.session(r)
	if !ok {
		redirect(w, "/admin/signin")
		return
	}
	if !s.principal(u).Can(auth.PermDecide) {
		s.denyRole(w, sess, u)
		return
	}
	list, err := s.o.Store.ListApprovals(r.Context(), s.o.Org, store.ApprovalPending, 100)
	if err != nil {
		s.log.Error("web: list approvals", "err", err)
		s.pageFor(w, http.StatusInternalServerError, sess, u, "Something went wrong", "The approvals could not be read. Try again.")
		return
	}
	now := s.now()
	d := pageData{Title: "Approvals", User: view(u), CSRF: sess.CSRF}
	for _, a := range list {
		if a.EffectiveStatus(now) != store.ApprovalPending {
			continue
		}
		if len(d.Approvals) == maxApprovalsShown {
			break
		}
		d.Approvals = append(d.Approvals, s.approvalView(r, a))
	}
	if f, ok := flashes[r.URL.Query().Get("result")]; ok {
		d.Flash = &f
	}
	s.audit(r.Context(), u, "approvals.view:"+strconv.Itoa(len(d.Approvals)))
	s.render(w, http.StatusOK, "approvals", d)
}

func (s *Server) approvalView(r *http.Request, a store.Approval) approvalView {
	v := approvalView{ID: a.ID, Tool: a.Tool, Server: a.ServerAlias, Person: a.RequesterPerson, Requester: a.RequesterName, Reason: a.Reason,
		Expires: a.ExpiresAt.UTC().Format(time.DateTime + " UTC")}
	raw, err := s.o.Approval.Arguments(r.Context(), s.o.Org, a)
	if err != nil {
		v.ArgsError = true
		return v
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		buf.Reset()
		buf.Write(raw)
	}
	v.ArgsBytes = buf.Len()
	text := buf.String()
	if len(text) > maxArgsShown {
		text, v.ArgsShortened = strings.ToValidUTF8(text[:maxArgsShown], ""), true
	}
	v.Args = text
	v.CanApprove = !v.ArgsShortened
	return v
}

// decide approves or denies one request. Checks run in a fixed order: origin,
// session, CSRF token, role, and only then the decision.
func (s *Server) decide(approve bool) http.HandlerFunc {
	verb := "deny"
	if approve {
		verb = "approve"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sess, u, ok := s.guarded(w, r)
		if !ok {
			return
		}
		if !s.principal(u).Can(auth.PermDecide) {
			s.denyRole(w, sess, u)
			return
		}
		id := r.PathValue("id")
		if !approvalIDRe.MatchString(id) {
			redirect(w, "/admin/approvals?result=gone")
			return
		}
		err := s.o.Approval.Decide(r.Context(), s.o.Org, id, approval.Decider{UserID: u.ID, Name: u.Person, Person: u.Person}, approve)
		switch {
		case err == nil:
			s.audit(r.Context(), u, "approval."+verb+":"+id)
			result := "denied"
			if approve {
				result = "approved"
			}
			redirect(w, "/admin/approvals?result="+result)
		case errors.Is(err, approval.ErrSelfApproval):
			ev := s.event("approval."+verb+":"+id, u.Person, "user:"+u.ID, "deny", "refused")
			if aerr := s.o.Audit.Admin(r.Context(), s.o.Org, ev); aerr != nil {
				s.log.Error("web: audit", "action", ev.Action, "err", aerr)
			}
			redirect(w, "/admin/approvals?result=self")
		case errors.Is(err, approval.ErrNotPending), errors.Is(err, store.ErrNotFound):
			redirect(w, "/admin/approvals?result=gone")
		default:
			s.log.Error("web: decide approval", "err", err)
			s.pageFor(w, http.StatusInternalServerError, sess, u, "Something went wrong", "The decision was not recorded. Try again.")
		}
	}
}
