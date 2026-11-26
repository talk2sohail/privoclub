package control

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/store"
)

// Cross-server flow tags endpoints (ADR 0032).

type flowTagView struct {
	ToolName  string    `json:"tool_name"`
	Tag       string    `json:"tag"`
	UpdatedAt time.Time `json:"updated_at"`
}

type flowTagInput struct {
	ToolName string `json:"tool_name"`
	Tag      string `json:"tag"`
}

func (a *API) getFlowTags(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	var acc *store.Access
	if !p.Can(auth.PermManageServers) && !p.Can(auth.PermManagePolicy) {
		a0, err := gateway.Access(r.Context(), a.Store, p)
		if err != nil {
			return err
		}
		if !a0.Reaches(s.ID) {
			return errorf(http.StatusNotFound, "no server named %q", r.PathValue("alias"))
		}
		acc = &a0
	}
	tags, err := a.Store.ListFlowTags(r.Context(), p.OrgID, s.ID)
	if err != nil {
		return err
	}
	out := make([]flowTagView, 0, len(tags))
	for _, t := range tags {
		if acc != nil && t.ToolName != "" && !acc.SeesTool(s.ID, t.ToolName) {
			continue
		}
		out = append(out, flowTagView{ToolName: t.ToolName, Tag: t.Tag, UpdatedAt: t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"server": s.Alias, "tags": out})
	return nil
}

func (a *API) putFlowTags(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Tags *[]flowTagInput `json:"tags"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Tags == nil {
		return errorf(http.StatusUnprocessableEntity, "tags list is required")
	}
	if len(*req.Tags) > 1000 {
		return errorf(http.StatusUnprocessableEntity, "at most 1000 flow tags per server")
	}
	tags := make([]store.FlowTag, 0, len(*req.Tags))
	for _, in := range *req.Tags {
		if !store.ValidFlowTag(in.Tag) {
			return errorf(http.StatusUnprocessableEntity, "tag must be untrusted_source or egress_sink")
		}
		if !validFlowToolName(in.ToolName) {
			return errorf(http.StatusUnprocessableEntity, "tool_name must be empty or at most 512 characters without control characters")
		}
		tags = append(tags, store.FlowTag{ServerID: s.ID, ToolName: in.ToolName, Tag: in.Tag})
	}
	if err := a.Store.SetFlowTags(r.Context(), p.OrgID, s.ID, tags); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return errorf(http.StatusNotFound, "no server named %q", r.PathValue("alias"))
		case errors.Is(err, store.ErrInvalidFlowTag):
			return errorf(http.StatusUnprocessableEntity, "tag must be untrusted_source or egress_sink")
		}
		return err
	}
	a.adminEvent(r.Context(), p, "server.flow_tags:"+s.Alias, s.Alias)
	stored, err := a.Store.ListFlowTags(r.Context(), p.OrgID, s.ID)
	if err != nil {
		return err
	}
	out := make([]flowTagView, 0, len(stored))
	for _, t := range stored {
		out = append(out, flowTagView{ToolName: t.ToolName, Tag: t.Tag, UpdatedAt: t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"server": s.Alias, "tags": out})
	return nil
}

func validFlowToolName(name string) bool {
	if name == "" {
		return true
	}
	if len(name) > 512 || !utf8.ValidString(name) || strings.TrimSpace(name) == "" {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
