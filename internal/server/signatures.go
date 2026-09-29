package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/fingerprint"
	"github.com/retreat-community/lanscape/internal/proto"
)

// SignatureSet is what the server distributes to agents on top of their built-in library (§8.2):
// signatures downloaded from URL (e.g. the project's current signatures.yaml, refreshed daily)
// and the administrator's own.
type SignatureSet struct {
	URL        string `json:"url,omitempty"`
	Custom     string `json:"custom,omitempty"` // YAML or JSON
	Fetched    string `json:"-"`                // last download, as JSON
	FetchedAt  int64  `json:"fetched_at"`       // unix ms
	FetchError string `json:"fetch_error,omitempty"`
}

// SignatureInfo is the API view of the set.
type SignatureInfo struct {
	SignatureSet
	Builtin      int `json:"builtin"`
	FetchedCount int `json:"fetched_count"`
	CustomCount  int `json:"custom_count"`
}

type storedSignatures struct {
	SignatureSet
	FetchedJSON string `json:"fetched,omitempty"`
}

func (s *Server) signatureSet(ctx context.Context) SignatureSet {
	var st storedSignatures
	_ = s.store.GetSetting(ctx, "signatures", &st)
	st.Fetched = st.FetchedJSON
	return st.SignatureSet
}

func (s *Server) saveSignatureSet(ctx context.Context, set SignatureSet) error {
	return s.store.SetSetting(ctx, "signatures", storedSignatures{SignatureSet: set, FetchedJSON: set.Fetched})
}

// compiled returns the downloaded and the custom signatures (custom ones win).
func (set SignatureSet) compiled() (fetched, custom []*fingerprint.Signature, err error) {
	if set.Fetched != "" {
		if fetched, err = fingerprint.Parse([]byte(set.Fetched)); err != nil {
			return nil, nil, err
		}
	}
	if strings.TrimSpace(set.Custom) != "" {
		if custom, err = fingerprint.Parse([]byte(set.Custom)); err != nil {
			return nil, nil, err
		}
	}
	return fetched, custom, nil
}

// payload is the JSON list sent to agents.
func (set SignatureSet) payload() (json.RawMessage, error) {
	fetched, custom, err := set.compiled()
	if err != nil {
		return nil, err
	}
	all := append(fetched, custom...)
	if len(all) == 0 {
		return json.RawMessage("[]"), nil
	}
	return json.Marshal(all)
}

// pushSignatures sends the set to one agent ("" = all online Full agents).
func (s *Server) pushSignatures(ctx context.Context, agentID string) {
	payload, err := s.signatureSet(ctx).payload()
	if err != nil {
		s.log.Warn("signatures not distributed", "err", err)
		return
	}
	for _, a := range s.hub.List() {
		if (agentID != "" && a.ID != agentID) || !a.Online || a.Kind == "lite" {
			continue
		}
		c, ok := s.hub.Conn(a.ID)
		if !ok {
			continue
		}
		go func(name string, c Conn) {
			rctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			defer cancel()
			if _, err := c.Request(rctx, proto.MsgConfig, proto.ConfigMsg{Signatures: payload}); err != nil {
				s.log.Debug("signatures not delivered", "agent", name, "err", err)
			}
		}(a.Name, c)
	}
}

// fetchSignatures downloads the set's URL (YAML or JSON) and distributes the result.
func (s *Server) fetchSignatures(ctx context.Context) error {
	set := s.signatureSet(ctx)
	if set.URL == "" {
		return fmt.Errorf("no signature URL configured")
	}
	set.FetchedAt = time.Now().UnixMilli()
	sigs, err := downloadSignatures(ctx, set.URL)
	if err != nil {
		set.FetchError = err.Error()
	} else {
		b, _ := json.Marshal(sigs)
		set.Fetched, set.FetchError = string(b), ""
	}
	if serr := s.saveSignatureSet(ctx, set); serr != nil {
		return serr
	}
	if err == nil {
		s.pushSignatures(ctx, "")
	}
	return err
}

func downloadSignatures(ctx context.Context, url string) ([]*fingerprint.Signature, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signatures: %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return fingerprint.Parse(b)
}

// signatureScheduler refreshes downloaded signatures once a day.
func (s *Server) signatureScheduler(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		set := s.signatureSet(ctx)
		if set.URL != "" && time.Since(time.UnixMilli(set.FetchedAt)) >= 24*time.Hour {
			if err := s.fetchSignatures(ctx); err != nil {
				s.log.Warn("signature update failed", "url", set.URL, "err", err)
			}
		}
	}
}

func (s *Server) signatureInfo(ctx context.Context) SignatureInfo {
	set := s.signatureSet(ctx)
	info := SignatureInfo{SignatureSet: set}
	if lib, err := fingerprint.Load(); err == nil {
		info.Builtin = len(lib.Sigs)
	}
	if f, c, err := set.compiled(); err == nil {
		info.FetchedCount, info.CustomCount = len(f), len(c)
	}
	return info
}

func (s *Server) apiSignatures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.signatureInfo(r.Context()))
}

func (s *Server) apiSaveSignatures(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		Custom string `json:"custom"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !validURL(req.URL) {
		writeError(w, http.StatusBadRequest, "the signature URL must start with http:// or https://")
		return
	}
	if strings.TrimSpace(req.Custom) != "" {
		if _, err := fingerprint.Parse([]byte(req.Custom)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	set := s.signatureSet(r.Context())
	if set.URL != req.URL {
		set.Fetched, set.FetchedAt, set.FetchError = "", 0, ""
	}
	set.URL, set.Custom = req.URL, req.Custom
	if err := s.saveSignatureSet(r.Context(), set); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "signatures.update", "", "ok", "")
	s.pushSignatures(r.Context(), "")
	writeJSON(w, http.StatusOK, s.signatureInfo(r.Context()))
}

func (s *Server) apiFetchSignatures(w http.ResponseWriter, r *http.Request) {
	if err := s.fetchSignatures(r.Context()); err != nil {
		s.audit(r, "signatures.fetch", "", "error", err.Error())
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.audit(r, "signatures.fetch", "", "ok", "")
	writeJSON(w, http.StatusOK, s.signatureInfo(r.Context()))
}
