package ihtml

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) registerTemplateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/templates/current", h.withUser(h.handleGetCurrentTemplate))
	mux.HandleFunc("PUT /api/templates/current", h.withJSONWrite(h.handlePutCurrentTemplate))
	mux.HandleFunc("GET /api/templates", h.withUser(h.handleListTemplates))
	mux.HandleFunc("POST /api/templates", h.withJSONWrite(h.handlePutTemplate))
	mux.HandleFunc("GET /api/templates/{id}", h.withUser(h.handleGetTemplate))
	mux.HandleFunc("PUT /api/templates/{id}", h.withJSONWrite(h.handlePutTemplate))
	mux.HandleFunc("DELETE /api/templates/{id}", h.withWrite(h.handleDeleteTemplate))
	mux.HandleFunc("POST /api/templates/{id}/publish", h.withJSONWrite(h.handlePublishTemplate))
	mux.HandleFunc("DELETE /api/templates/{id}/slots/{slot}", h.withWrite(h.handleDeleteTemplateSlot))
}

func (h *Handler) handleListTemplates(w http.ResponseWriter, r *http.Request, user string) {
	templates, err := h.ListTemplates(r.Context(), user)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if templates == nil {
		templates = []Template{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}

func (h *Handler) handleGetTemplate(w http.ResponseWriter, r *http.Request, user string) {
	template, err := h.GetTemplate(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": template})
}

func (h *Handler) handleGetCurrentTemplate(w http.ResponseWriter, r *http.Request, user string) {
	current, err := h.GetCurrentTemplate(r.Context(), user, r.URL.Query().Get("page"))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, current)
}

type templateWritePayload struct {
	Page             string   `json:"page,omitempty"`
	Template         Template `json:"template"`
	ExpectedRevision uint64   `json:"expected_revision"`
}

func validateTemplatePayloadShape(template Template) error {
	if strings.TrimSpace(template.ID) == "" {
		return fmt.Errorf("template.id is required; send the GET response's template field, not the outer current-template wrapper")
	}
	if strings.TrimSpace(template.Name) == "" {
		return fmt.Errorf("template.name is required")
	}
	if template.Draft.Nodes == nil || template.Draft.Slots == nil || template.Draft.Placements == nil || template.Draft.Breakpoints == nil {
		return fmt.Errorf("template.draft nodes, slots, placements, and breakpoints arrays are required (empty arrays are valid)")
	}
	return nil
}

func writeTemplateError(w http.ResponseWriter, err error) {
	var conflict *TemplateConflictError
	if errors.As(err, &conflict) {
		out := map[string]any{
			"error":             err.Error(),
			"expected_revision": conflict.Expected,
			"current_revision":  conflict.Actual,
		}
		if conflict.Current != nil {
			out["current"] = conflict.Current
		}
		writeJSON(w, http.StatusConflict, out)
		return
	}
	status := http.StatusBadRequest
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func (h *Handler) handlePutTemplate(w http.ResponseWriter, r *http.Request, user string) {
	var payload templateWritePayload
	if err := readJSON(w, r, 2<<20, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if id := r.PathValue("id"); id != "" {
		if payload.Template.ID != "" && payload.Template.ID != id {
			h.writeError(w, http.StatusBadRequest, fmt.Errorf("path and template ids differ"))
			return
		}
		payload.Template.ID = id
	}
	if err := validateTemplatePayloadShape(payload.Template); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	template, err := h.PutTemplate(r.Context(), user, payload.Template, payload.ExpectedRevision, "api")
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	status := http.StatusOK
	if payload.ExpectedRevision == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"template": template})
}

func (h *Handler) handlePutCurrentTemplate(w http.ResponseWriter, r *http.Request, user string) {
	var payload templateWritePayload
	if err := readJSON(w, r, 2<<20, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if err := validateTemplatePayloadShape(payload.Template); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	page := r.URL.Query().Get("page")
	if queryHasPage := r.URL.Query().Has("page"); !queryHasPage {
		page = payload.Page
	} else if payload.Page != "" && payload.Page != page {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("query and body pages differ"))
		return
	}
	template, err := h.PutCurrentTemplate(r.Context(), user, page, payload.Template, payload.ExpectedRevision, "api")
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": template})
}

func expectedRevisionFromQuery(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("expected_revision"))
	if raw == "" {
		return 0, fmt.Errorf("expected_revision is required")
	}
	revision, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid expected_revision: %w", err)
	}
	return revision, nil
}

func (h *Handler) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, user string) {
	expected, err := expectedRevisionFromQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.DeleteTemplate(r.Context(), user, r.PathValue("id"), expected, "api"); err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handlePublishTemplate(w http.ResponseWriter, r *http.Request, user string) {
	var payload struct {
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if err := readJSON(w, r, 64<<10, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	template, err := h.PublishTemplate(r.Context(), user, r.PathValue("id"), payload.ExpectedRevision, "api")
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": template})
}

func (h *Handler) handleDeleteTemplateSlot(w http.ResponseWriter, r *http.Request, user string) {
	expected, err := expectedRevisionFromQuery(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	template, err := h.DeleteTemplateSlot(r.Context(), user, r.PathValue("id"), r.PathValue("slot"), expected, "api")
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": template})
}
