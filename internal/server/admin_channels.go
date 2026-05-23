package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// adminChannelDTO is the admin-facing channel shape. Unlike the user-facing
// channelDTO (Phase 6), this surfaces the (src, admin, effective) triplet so
// the SPA can show "what the upstream said" vs "what we override". User
// endpoints continue to return only the effective values.
type adminChannelDTO struct {
	ID                 string  `json:"id"`
	SourceM3UID        string  `json:"source_m3u_id"`
	SourceChannelID    string  `json:"source_channel_id"`
	DisplayName        string  `json:"display_name"`
	ChannelNumberSrc   string  `json:"channel_number_src"`
	ChannelNumberAdmin *string `json:"channel_number_admin,omitempty"`
	ChannelNumberEff   string  `json:"channel_number_effective"`
	LogoURL            string  `json:"logo_url"`
	GroupTitleSrc      string  `json:"group_title_src"`
	GroupTitleAdmin    *string `json:"group_title_admin,omitempty"`
	GroupTitleEff      string  `json:"group_title_effective"`
	UpstreamURL        string  `json:"upstream_url"`
	UpstreamKind       string  `json:"upstream_kind"`
	EnabledSrc         bool    `json:"enabled_src"`
	EnabledAdmin       *bool   `json:"enabled_admin,omitempty"`
	EnabledEff         bool    `json:"enabled_effective"`
	Position           int     `json:"position"`
}

// toAdminChannelDTO collapses a store.Channel onto the admin wire shape.
func toAdminChannelDTO(c store.Channel) adminChannelDTO {
	return adminChannelDTO{
		ID:                 c.ID,
		SourceM3UID:        c.SourceM3UID,
		SourceChannelID:    c.SourceChannelID,
		DisplayName:        c.DisplayName,
		ChannelNumberSrc:   c.ChannelNumberSrc,
		ChannelNumberAdmin: c.ChannelNumberAdmin,
		ChannelNumberEff:   c.EffectiveChannelNum,
		LogoURL:            c.LogoURL,
		GroupTitleSrc:      c.GroupTitleSrc,
		GroupTitleAdmin:    c.GroupTitleAdmin,
		GroupTitleEff:      c.EffectiveGroupTitle,
		UpstreamURL:        c.UpstreamURL,
		UpstreamKind:       c.UpstreamKind,
		EnabledSrc:         c.EnabledSrc,
		EnabledAdmin:       c.EnabledAdmin,
		EnabledEff:         c.EffectiveEnabled,
		Position:           c.Position,
	}
}

// patchField encodes the three-valued semantics needed for PATCH bodies:
//   - missing key            → Set=false, leave the column untouched
//   - {"set":true,"value":x} → Set=true, apply x (nil clears the override)
//
// Generic so the channel patch can reuse one structure across pointer types.
type patchField[T any] struct {
	Set   bool `json:"set"`
	Value T    `json:"value"`
}

// adminChannelPatch is the PATCH body. Each field is independent; omit a key
// to leave it unchanged.
type adminChannelPatch struct {
	ChannelNumberAdmin patchField[*string] `json:"channel_number_admin"`
	GroupTitleAdmin    patchField[*string] `json:"group_title_admin"`
	EnabledAdmin       patchField[*bool]   `json:"enabled_admin"`
	Position           patchField[*int]    `json:"position"`
}

// toStorePatch converts the wire-format patch to the store.ChannelPatch type
// expected by Store.AdminPatchChannel. The two shapes are intentionally
// parallel so this mapping is a 1:1 field copy.
func (p adminChannelPatch) toStorePatch() store.ChannelPatch {
	out := store.ChannelPatch{}
	if p.ChannelNumberAdmin.Set {
		out.ChannelNumberAdmin = store.SetChannelNumberAdmin(p.ChannelNumberAdmin.Value)
	}
	if p.GroupTitleAdmin.Set {
		out.GroupTitleAdmin = store.SetGroupTitleAdmin(p.GroupTitleAdmin.Value)
	}
	if p.EnabledAdmin.Set {
		out.EnabledAdmin = store.SetEnabledAdmin(p.EnabledAdmin.Value)
	}
	if p.Position.Set {
		out.Position = store.SetPosition(p.Position.Value)
	}
	return out
}

// addEPGKeyRequest is the POST body for /admin/channels/{id}/epg-keys.
type addEPGKeyRequest struct {
	XMLTVChannelID string `json:"xmltv_channel_id"`
}

// epgKeysEnvelope wraps the GET response. Kept distinct from listEnvelope so
// the field name "xmltv_channel_ids" is explicit on the wire.
type epgKeysEnvelope struct {
	XMLTVChannelIDs []string `json:"xmltv_channel_ids"`
}

// mountAdminChannels registers the /admin/channels routes. Called from
// Routes() once the admin chi subrouter is built.
func (s *Server) mountAdminChannels(r chi.Router) {
	r.Get("/channels", s.adminListChannels)
	r.Patch("/channels/{id}", s.adminPatchChannel)
	r.Get("/channels/{id}/epg-keys", s.adminListEPGKeys)
	r.Post("/channels/{id}/epg-keys", s.adminAddEPGKey)
	r.Delete("/channels/{id}/epg-keys/{xmltv_channel_id}", s.adminRemoveEPGKey)
}

// adminListChannels handles GET /admin/channels?source_m3u_id=...
func (s *Server) adminListChannels(w http.ResponseWriter, r *http.Request) {
	sourceID := r.URL.Query().Get("source_m3u_id")
	channels, err := s.Store.AdminListChannels(r.Context(), sourceID)
	if err != nil {
		s.logger().Warn("admin list channels", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dtos := make([]adminChannelDTO, len(channels))
	for i, c := range channels {
		dtos[i] = toAdminChannelDTO(c)
	}
	writeJSON(w, http.StatusOK, listEnvelope[adminChannelDTO]{Data: dtos})
}

// adminPatchChannel handles PATCH /admin/channels/{id}. Applies admin
// overrides and returns the updated admin DTO.
func (s *Server) adminPatchChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErrorMsg(w, http.StatusBadRequest, "id required")
		return
	}
	var patch adminChannelPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := s.Store.AdminPatchChannel(r.Context(), id, patch.toStorePatch()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "channel not found")
			return
		}
		s.logger().Warn("admin patch channel", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ch, err := s.Store.GetChannel(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "channel not found")
			return
		}
		s.logger().Warn("admin reload channel", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminChannelDTO(ch))
}

// adminListEPGKeys handles GET /admin/channels/{id}/epg-keys.
func (s *Server) adminListEPGKeys(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	keys, err := s.Store.ListEPGKeys(r.Context(), id)
	if err != nil {
		s.logger().Warn("admin list epg keys", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if keys == nil {
		keys = []string{}
	}
	writeJSON(w, http.StatusOK, epgKeysEnvelope{XMLTVChannelIDs: keys})
}

// adminAddEPGKey handles POST /admin/channels/{id}/epg-keys with
// {"xmltv_channel_id": "..."}. Manual links are inserted with
// auto_linked=false so subsequent XMLTV refreshes don't clobber them.
func (s *Server) adminAddEPGKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req addEPGKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.XMLTVChannelID == "" {
		writeErrorMsg(w, http.StatusBadRequest, "xmltv_channel_id required")
		return
	}
	if err := s.Store.AddEPGKey(r.Context(), id, req.XMLTVChannelID, false); err != nil {
		s.logger().Warn("admin add epg key", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminRemoveEPGKey handles DELETE /admin/channels/{id}/epg-keys/{xmltv}.
func (s *Server) adminRemoveEPGKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	key := chi.URLParam(r, "xmltv_channel_id")
	if err := s.Store.RemoveEPGKey(r.Context(), id, key); err != nil {
		s.logger().Warn("admin remove epg key", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
