package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deigmata-paideias/gateway/internal/config"
	"github.com/deigmata-paideias/gateway/internal/gateway"
	"github.com/deigmata-paideias/gateway/internal/model"
)

type adminAPI struct {
	service      *gateway.Service
	maxBodyBytes int64
}

func NewAdminHandler(service *gateway.Service) http.Handler {
	api := &adminAPI{service: service, maxBodyBytes: service.CurrentSnapshot().Config().Limits.RequestBodyBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/v1/config", api.getConfig)
	mux.HandleFunc("PUT /admin/v1/config", api.putConfig)
	mux.HandleFunc("GET /admin/v1/backends", api.backends)
	mux.HandleFunc("PUT /admin/v1/backends/{backend_id}", api.putBackend)
	mux.HandleFunc("DELETE /admin/v1/backends/{backend_id}", api.deleteBackend)
	mux.HandleFunc("PUT /admin/v1/routes/{route_id}/active-backend", api.switchBackend)
	mux.HandleFunc("GET /admin/v1/revisions", api.revisions)
	mux.HandleFunc("GET /admin/v1/revisions/{revision}", api.revision)
	mux.HandleFunc("POST /admin/v1/revisions/{revision}/restore", api.restoreRevision)

	mux.HandleFunc("POST /admin/v1/credentials", api.createCredential)
	mux.HandleFunc("GET /admin/v1/credentials", api.credentials)
	mux.HandleFunc("GET /admin/v1/credentials/{credential_id}", api.credential)
	mux.HandleFunc("POST /admin/v1/credentials/{credential_id}/rotate", api.rotateCredential)
	mux.HandleFunc("DELETE /admin/v1/credentials/{credential_id}", api.deleteCredential)

	mux.HandleFunc("POST /admin/v1/tokens", api.createToken)
	mux.HandleFunc("GET /admin/v1/tokens", api.tokens)
	mux.HandleFunc("GET /admin/v1/tokens/{token_id}", api.token)
	mux.HandleFunc("GET /admin/v1/tokens/{token_id}/secret", api.tokenSecret)
	mux.HandleFunc("POST /admin/v1/tokens/{token_id}/rotate", api.rotateToken)
	mux.HandleFunc("POST /admin/v1/tokens/{token_id}/revoke", api.revokeToken)
	mux.HandleFunc("DELETE /admin/v1/tokens/{token_id}", api.deleteToken)
	mux.HandleFunc("GET /admin/v1/tokens/{token_id}/usage", api.tokenUsage)

	mux.HandleFunc("GET /admin/v1/audits", api.audits)
	mux.HandleFunc("GET /admin/v1/audits/{audit_id}", api.audit)
	return requestIDMiddleware(mux)
}

func (a *adminAPI) getConfig(w http.ResponseWriter, _ *http.Request) {
	revision, gatewayConfig := a.service.Config()
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "config": gatewayConfig})
}

func (a *adminAPI) putConfig(w http.ResponseWriter, r *http.Request) {
	expected, err := parseIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	body, err := readBody(r, a.maxBodyBytes)
	if err != nil {
		writeError(w, err)
		return
	}
	var candidate config.Gateway
	if err := json.Unmarshal(body, &candidate); err != nil {
		writeError(w, &gateway.Error{Status: http.StatusBadRequest, Code: "invalid_json", Message: "配置 JSON 无效", Err: err})
		return
	}
	revision, err := a.service.UpdateConfig(r.Context(), expected, candidate, "replace", requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "config": candidate})
}

func (a *adminAPI) backends(w http.ResponseWriter, _ *http.Request) {
	revision, gatewayConfig := a.service.Config()
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "data": gatewayConfig.Backends})
}

func (a *adminAPI) putBackend(w http.ResponseWriter, r *http.Request) {
	expected, err := parseIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var backend config.Backend
	if err := decodeJSON(r, a.maxBodyBytes, &backend); err != nil {
		writeError(w, err)
		return
	}
	backend.ID = r.PathValue("backend_id")
	_, current := a.service.Config()
	replaced := false
	for index := range current.Backends {
		if current.Backends[index].ID == backend.ID {
			current.Backends[index] = backend
			replaced = true
			break
		}
	}
	if !replaced {
		current.Backends = append(current.Backends, backend)
	}
	revision, err := a.service.UpdateConfig(r.Context(), expected, current, "put_backend", requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "backend": backend})
}

func (a *adminAPI) deleteBackend(w http.ResponseWriter, r *http.Request) {
	expected, err := parseIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("backend_id")
	_, current := a.service.Config()
	for _, route := range current.Routes {
		for _, target := range route.Targets {
			if target.BackendID == id {
				writeError(w, &gateway.Error{Status: http.StatusConflict, Code: "backend_in_use", Message: "Backend 仍被 Route 引用", Err: errors.New("backend in use")})
				return
			}
		}
	}
	filtered := make([]config.Backend, 0, len(current.Backends))
	for _, backend := range current.Backends {
		if backend.ID != id {
			filtered = append(filtered, backend)
		}
	}
	if len(filtered) == len(current.Backends) {
		writeError(w, &gateway.Error{Status: http.StatusNotFound, Code: "not_found", Message: "Backend 不存在", Err: errors.New("backend not found")})
		return
	}
	current.Backends = filtered
	revision, err := a.service.UpdateConfig(r.Context(), expected, current, "delete_backend", requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (a *adminAPI) switchBackend(w http.ResponseWriter, r *http.Request) {
	expected, err := parseIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		BackendID string `json:"backend_id"`
	}
	if err := decodeJSON(r, a.maxBodyBytes, &body); err != nil {
		writeError(w, err)
		return
	}
	revision, err := a.service.SwitchBackend(
		r.Context(), expected, r.PathValue("route_id"), body.BackendID, requestIDFrom(r.Context()),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "active_backend": body.BackendID})
}

func (a *adminAPI) revisions(w http.ResponseWriter, r *http.Request) {
	items, err := a.service.ConfigRevisions(r.Context(), parseLimit(r, 100))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (a *adminAPI) revision(w http.ResponseWriter, r *http.Request) {
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil {
		writeError(w, badParameter("revision", err))
		return
	}
	item, err := a.service.Store().ConfigRevision(r.Context(), revision)
	if err != nil {
		writeError(w, &gateway.Error{Status: http.StatusNotFound, Code: "not_found", Message: "Revision 不存在", Err: err})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *adminAPI) restoreRevision(w http.ResponseWriter, r *http.Request) {
	expected, err := parseIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	source, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil {
		writeError(w, badParameter("revision", err))
		return
	}
	revision, err := a.service.RestoreConfig(r.Context(), expected, source, requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "restored_from": source})
}

func (a *adminAPI) createCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Secret   string `json:"secret"`
	}
	if err := decodeJSON(r, 32<<10, &body); err != nil {
		writeError(w, err)
		return
	}
	credential, err := a.service.CreateCredential(r.Context(), gateway.CredentialInput{
		ID: body.ID, Provider: body.Provider, Name: body.Name, Secret: []byte(body.Secret),
	}, requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, credentialView(credential))
}

func (a *adminAPI) credentials(w http.ResponseWriter, r *http.Request) {
	items, err := a.service.Credentials(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	views := make([]credentialResponse, 0, len(items))
	for _, item := range items {
		views = append(views, credentialView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

func (a *adminAPI) credential(w http.ResponseWriter, r *http.Request) {
	item, err := a.service.Credential(r.Context(), r.PathValue("credential_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialView(item))
}

func (a *adminAPI) rotateCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"secret"`
	}
	if err := decodeJSON(r, 32<<10, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := a.service.RotateCredential(
		r.Context(), r.PathValue("credential_id"), []byte(body.Secret), requestIDFrom(r.Context()),
	); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "rotated"})
}

func (a *adminAPI) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := a.service.DeleteCredential(r.Context(), r.PathValue("credential_id"), requestIDFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (a *adminAPI) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		ExpiresAt *int64 `json:"expires_at"`
	}
	if err := decodeJSON(r, 16<<10, &body); err != nil {
		writeError(w, err)
		return
	}
	issued, err := a.service.CreateToken(r.Context(), body.Name, body.ExpiresAt, requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	response := tokenView(issued.Token)
	response.Token = issued.Secret
	setNoStore(w)
	writeJSON(w, http.StatusCreated, response)
}

func (a *adminAPI) tokens(w http.ResponseWriter, r *http.Request) {
	items, err := a.service.Tokens(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	views := make([]tokenResponse, 0, len(items))
	for _, item := range items {
		views = append(views, tokenView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

func (a *adminAPI) token(w http.ResponseWriter, r *http.Request) {
	item, err := a.service.Token(r.Context(), r.PathValue("token_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenView(item))
}

func (a *adminAPI) tokenSecret(w http.ResponseWriter, r *http.Request) {
	value, err := a.service.TokenSecret(r.Context(), r.PathValue("token_id"), requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("token_id"), "token": value})
}

func (a *adminAPI) rotateToken(w http.ResponseWriter, r *http.Request) {
	issued, err := a.service.RotateToken(r.Context(), r.PathValue("token_id"), requestIDFrom(r.Context()))
	if err != nil {
		writeError(w, err)
		return
	}
	response := tokenView(issued.Token)
	response.Token = issued.Secret
	setNoStore(w)
	writeJSON(w, http.StatusOK, response)
}

func (a *adminAPI) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := a.service.RevokeToken(r.Context(), r.PathValue("token_id"), requestIDFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (a *adminAPI) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := a.service.DeleteToken(r.Context(), r.PathValue("token_id"), requestIDFrom(r.Context())); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (a *adminAPI) audits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := model.AuditFilter{
		TokenID: query.Get("token_id"), Operation: query.Get("operation"), ModelAlias: query.Get("model_alias"),
		BackendID: query.Get("backend_id"), Status: query.Get("status"), Limit: parseLimit(r, 100), BeforeID: query.Get("cursor"),
	}
	var err error
	filter.From, err = parseTimeQuery(query.Get("from"))
	if err == nil {
		filter.To, err = parseTimeQuery(query.Get("to"))
	}
	if err != nil {
		writeError(w, badParameter("time", err))
		return
	}
	items, err := a.service.Store().Audits(r.Context(), filter)
	if err != nil {
		writeError(w, &gateway.Error{Status: http.StatusServiceUnavailable, Code: "storage_unavailable", Message: "审计查询失败", Err: err})
		return
	}
	nextCursor := ""
	if len(items) == filter.Limit && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "next_cursor": nextCursor})
}

func (a *adminAPI) audit(w http.ResponseWriter, r *http.Request) {
	item, err := a.service.Store().Audit(r.Context(), r.PathValue("audit_id"))
	if err != nil {
		writeError(w, &gateway.Error{Status: http.StatusNotFound, Code: "not_found", Message: "审计记录不存在", Err: err})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *adminAPI) tokenUsage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, err := parseTimeQuery(query.Get("from"))
	if err == nil {
		var to *int64
		to, err = parseTimeQuery(query.Get("to"))
		if err == nil {
			groups, aggregateErr := a.service.Store().AggregateUsage(
				r.Context(), r.PathValue("token_id"), query.Get("group_by"), from, to,
			)
			if aggregateErr != nil {
				writeError(w, &gateway.Error{Status: http.StatusUnprocessableEntity, Code: "invalid_group_by", Message: "Usage 查询参数无效", Err: aggregateErr})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"token_id": r.PathValue("token_id"), "from": from, "to": to,
				"group_by": query.Get("group_by"), "groups": groups,
			})
			return
		}
	}
	writeError(w, badParameter("time", err))
}

func parseIfMatch(r *http.Request) (int64, error) {
	value := strings.Trim(r.Header.Get("If-Match"), "\"")
	if value == "" {
		return 0, &gateway.Error{Status: http.StatusPreconditionRequired, Code: "if_match_required", Message: "必须提供 If-Match Revision", Err: errors.New("缺少 if-match")}
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 {
		return 0, badParameter("If-Match", err)
	}
	return revision, nil
}

func parseLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value < 1 || value > 500 {
		return fallback
	}
	return value
}

func parseTimeQuery(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	if milliseconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return &milliseconds, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("时间 %q 无效", value)
	}
	milliseconds := parsed.UnixMilli()
	return &milliseconds, nil
}

func badParameter(name string, err error) *gateway.Error {
	if err == nil {
		err = errors.New("参数无效")
	}
	return &gateway.Error{Status: http.StatusBadRequest, Code: "invalid_parameter", Message: name + " 参数无效", Err: err}
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
