package collie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type peerCtxKey struct{}

// peerRoutes registers two strictly separated route surfaces:
//  1. `/api/peer/*`: narrow peer-to-peer endpoints authenticated ONLY by
//     `peerAuth` (Bearer clp_... hash in `peers` table) or a 10-min one-time
//     invite code. Zero access to terminals, files, memory, workers, or settings.
//  2. `/api/super/peers*`: owner management & confirmed-send endpoints
//     protected by the standard `s.auth` middleware.
func (s *Server) peerRoutes(mux *http.ServeMux) {
	// Narrow peer-to-peer surface (isolated from s.auth).
	mux.HandleFunc("POST /api/peer/pair/accept", s.peerPairAccept)
	mux.Handle("POST /api/peer/pair/activate", s.peerAuth(s.peerPairActivate, peerAuthActivate))
	mux.Handle("GET /api/peer/ping", s.peerAuth(s.peerPing, peerAuthPing))
	mux.Handle("POST /api/peer/inbox", s.peerAuth(s.peerInbox, peerAuthInbox))
	mux.Handle("POST /api/peer/revoke", s.peerAuth(s.peerRevokeInbound, peerAuthRevoke))

	// Owner management surface (standard s.auth).
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.auth(h)) }
	api("GET /api/super/peers", s.superPeersList)
	api("POST /api/super/peers/settings", s.superPeersSettings)
	api("POST /api/super/peers/invite", s.superPeersInvite)
	api("POST /api/super/peers/pair", s.superPeersPair)
	api("POST /api/super/peers/{id}/approve", s.superPeersApprove)
	api("POST /api/super/peers/{id}/reject", s.superPeersReject)
	api("PATCH /api/super/peers/{id}", s.superPeersUpdate)
	api("POST /api/super/peers/{id}/ping", s.superPeersPing)
	api("DELETE /api/super/peers/{id}", s.superPeersDelete)
	api("POST /api/super/peers/{id}/send", s.superPeersSend)
	api("GET /api/super/peers/{id}/messages", s.superPeersMessages)
	api("DELETE /api/super/peers/blocklist/{hub_id}", s.superPeersUnblock)
}

// peerAuth strictly validates `Authorization: Bearer clp_...` against the
// isolated SQLite `peers` table. It never reads cookies or query parameters,
// never grants access to any non-peer endpoint, and delays failed attempts.
func (s *Server) peerAuth(h http.HandlerFunc, scope peerAuthScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.sup == nil || s.sup.peers == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer inbox unavailable"})
			return
		}
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			if authFailDelay > 0 {
				time.Sleep(authFailDelay)
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		tok := strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
		res := s.sup.peers.Authenticate(tok, scope)
		if res.statusCode != http.StatusOK {
			if res.statusCode == http.StatusUnauthorized && authFailDelay > 0 {
				time.Sleep(authFailDelay)
			}
			writeJSON(w, res.statusCode, map[string]any{"error": res.errMsg})
			return
		}
		ctx := context.WithValue(r.Context(), peerCtxKey{}, res.peer)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func peerFromCtx(r *http.Request) *Peer {
	p, _ := r.Context().Value(peerCtxKey{}).(*Peer)
	return p
}

// peerAddrs computes the reachable HTTP(S) base URLs to advertise in peer
// invites and handshakes, reusing lanAddresses() with loopback suppressed
// whenever non-loopback LAN/Tailscale or explicit -url addresses exist.
func (s *Server) peerAddrs(r *http.Request) []string {
	var raw []string
	for _, u := range s.publicURLs {
		if norm, ok := sanitizePeerURL(u); ok {
			raw = append(raw, norm)
		}
	}
	if s.port > 0 {
		detected := s.addresses(r)
		var nonLoop, loop []string
		for _, a := range detected {
			norm, ok := sanitizePeerURL(a)
			if !ok {
				continue
			}
			host := strings.TrimPrefix(strings.TrimPrefix(norm, "https://"), "http://")
			if isLoopbackHost(host) {
				loop = append(loop, norm)
			} else {
				nonLoop = append(nonLoop, norm)
			}
		}
		if len(raw) > 0 {
			onlyLoopbackExplicit := true
			for _, u := range raw {
				host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
				if !isLoopbackHost(host) {
					onlyLoopbackExplicit = false
					break
				}
			}
			if !onlyLoopbackExplicit {
				raw = append(raw, nonLoop...)
			}
		} else if len(nonLoop) > 0 {
			raw = append(raw, nonLoop...)
		} else {
			raw = append(raw, loop...)
		}
	} else if len(raw) == 0 && r != nil && r.Host != "" {
		scheme := "http://"
		if r.TLS != nil {
			scheme = "https://"
		}
		if norm, ok := sanitizePeerURL(scheme + r.Host); ok {
			raw = append(raw, norm)
		}
	}
	return sanitizePeerAddrs(raw, 4)
}

// ---------------------------------------------------------------- /api/peer/* handlers

func (s *Server) peerPairAccept(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer inbox unavailable"})
		return
	}
	var req peerPairAcceptReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	resp, code, err := s.sup.peers.AcceptPair(r.Context(), req, s.peerAddrs(r))
	if err != nil {
		if code == http.StatusUnauthorized && pairFailDelay > 0 {
			time.Sleep(pairFailDelay)
		}
		writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) peerPairActivate(w http.ResponseWriter, r *http.Request) {
	p := peerFromCtx(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var req peerPairActivateReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	resp, code, err := s.sup.peers.ActivateOutgoingPeer(p, req, s.peerAddrs(r))
	if err != nil {
		writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) peerPing(w http.ResponseWriter, r *http.Request) {
	_, hubName, enabled := s.sup.peers.Identity()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"hub_name":     hubName,
		"version":      version,
		"protocol_ver": peerProtocolVer,
		"paused":       !enabled,
	})
}

func (s *Server) peerInbox(w http.ResponseWriter, r *http.Request) {
	p := peerFromCtx(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var payload PeerInboxPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, peerMaxReqBytes)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid or oversized JSON body (max 32 KB)"})
		return
	}
	resp, code := s.sup.peers.HandleInbound(p, payload)
	if code == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "15")
	}
	writeJSON(w, code, resp)
}

func (s *Server) peerRevokeInbound(w http.ResponseWriter, r *http.Request) {
	p := peerFromCtx(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if err := s.sup.peers.HandleRemoteRevoke(p, body.Reason); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- /api/super/peers* handlers

func (s *Server) superPeersList(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	hubID, hubName, enabled := s.sup.peers.Identity()
	peers, err := s.sup.peers.ListViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	blocked, err := s.sup.peers.ListBlocklist()
	if err != nil {
		blocked = []PeerBlockEntry{}
	}
	writeJSONETag(w, r, map[string]any{
		"hub_id":        hubID,
		"hub_name":      hubName,
		"enabled":       enabled,
		"addrs":         s.peerAddrs(r),
		"peers":         peers,
		"blocklist":     blocked,
		"blocked_count": len(blocked),
		"summary":       s.sup.peers.Summary(),
	})
}

func (s *Server) superPeersSettings(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		Enabled *bool   `json:"enabled"`
		HubName *string `json:"hub_name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name, enabled, err := s.sup.peers.UpdateSettings(body.Enabled, body.HubName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"hub_name": name,
		"enabled":  enabled,
		"summary":  s.sup.peers.Summary(),
	})
}

func (s *Server) superPeersInvite(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		Mode  string `json:"mode"`
		Reset bool   `json:"reset"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	res, err := s.sup.peers.CreateInviteWithReset(body.Mode, s.peerAddrs(r), body.Reset)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) superPeersPair(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		InviteCode string `json:"invite_code"`
		Name       string `json:"name"`
		Mode       string `json:"mode"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	view, code, err := s.sup.peers.PairWithInvite(ctx, body.InviteCode, body.Name, body.Mode, s.peerAddrs(r))
	if err != nil {
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"peer":    view,
		"pending": view != nil && view.Status == "pending_outgoing",
	})
}

func (s *Server) superPeersApprove(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	view, code, err := s.sup.peers.ApprovePeer(ctx, r.PathValue("id"), body.Name, body.Mode, s.peerAddrs(r))
	if err != nil {
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "peer": view})
}

func (s *Server) superPeersReject(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		Block bool `json:"block"`
	}
	raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
	}
	block := body.Block || r.URL.Query().Get("block") == "1" || strings.EqualFold(r.URL.Query().Get("block"), "true")
	blocked, code, err := s.sup.peers.RejectPeer(r.Context(), r.PathValue("id"), block)
	if err != nil {
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked": blocked})
}

func (s *Server) superPeersUpdate(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var body struct {
		Name   *string `json:"name"`
		Mode   *string `json:"mode"`
		Status *string `json:"status"`
	}
	if !decode(w, r, &body) {
		return
	}
	view, blocked, code, err := s.sup.peers.UpdatePeer(r.Context(), r.PathValue("id"), body.Name, body.Mode, body.Status)
	if err != nil {
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "peer": view, "blocked": blocked})
}

func (s *Server) superPeersPing(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res, code := s.sup.peers.PingPeer(ctx, r.PathValue("id"))
	writeJSON(w, code, res)
}

func (s *Server) superPeersDelete(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	block := r.URL.Query().Get("block") == "1" || strings.EqualFold(r.URL.Query().Get("block"), "true")
	blocked, code, err := s.sup.peers.RevokePeer(r.Context(), r.PathValue("id"), block)
	if err != nil {
		writeJSON(w, code, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked": blocked})
}

func (s *Server) superPeersSend(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	var req PeerSendRequest
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, code, err := s.sup.peers.SendToPeer(ctx, r.PathValue("id"), req, s.token)
	if err != nil {
		msg := err.Error()
		if s.sup.secrets != nil {
			msg = s.sup.secrets.Redact(msg)
		}
		writeJSON(w, code, map[string]any{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) superPeersMessages(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := s.sup.peers.ListPeerMessages(r.PathValue("id"), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) superPeersUnblock(w http.ResponseWriter, r *http.Request) {
	if s.sup == nil || s.sup.peers == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "peer manager not ready"})
		return
	}
	removed, err := s.sup.peers.UnblockHub(r.PathValue("hub_id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "黑名单中未找到该 Hub"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
