package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
)

var vapidMu sync.Mutex

// vapid returns the server's Web Push identity, creating it on first use.
func (s *Server) vapid(ctx context.Context) (*notify.VAPID, error) {
	vapidMu.Lock()
	defer vapidMu.Unlock()
	var st struct {
		Private string `json:"private"`
		Subject string `json:"subject"`
	}
	if err := s.store.GetSetting(ctx, "vapid", &st); err == nil && st.Private != "" {
		return notify.ParseVAPID(st.Private, st.Subject)
	}
	subject := "https://github.com/retreat-community/lanscape"
	if pu := s.settings(ctx).PublicURL; strings.HasPrefix(pu, "https://") {
		subject = pu
	}
	v, err := notify.NewVAPID(subject)
	if err != nil {
		return nil, err
	}
	st.Private, st.Subject = v.MarshalPrivate(), v.Subject
	if err := s.store.SetSetting(ctx, "vapid", st); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Server) apiPushKey(w http.ResponseWriter, r *http.Request) {
	v, err := s.vapid(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": v.PublicKey()})
}

func (s *Server) apiPushSubscribe(w http.ResponseWriter, r *http.Request) {
	var sub notify.Subscription
	if !readJSON(w, r, &sub) {
		return
	}
	if !strings.HasPrefix(sub.Endpoint, "https://") || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		writeError(w, http.StatusBadRequest, "not a push subscription")
		return
	}
	if _, err := notify.Encrypt(sub, []byte("check")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, _ := principal(r.Context())
	err := s.store.SavePushSubscription(r.Context(), store.PushSubscription{UserID: p.User.ID, Endpoint: sub.Endpoint,
		P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth, UserAgent: r.UserAgent()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "subscribed"})
}

func (s *Server) apiPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	subs, _ := s.store.PushSubscriptions(r.Context(), p.User.ID)
	for _, sub := range subs {
		if sub.Endpoint == req.Endpoint {
			_ = s.store.DeletePushSubscription(r.Context(), req.Endpoint)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiPushTest(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	n := s.webPush(r.Context(), notify.Message{Event: "test", Severity: notify.SevInfo, Title: "Lanscape",
		Text: "Notifications work on this device.", At: time.Now().UnixMilli()}, p.User.ID)
	writeJSON(w, http.StatusOK, map[string]int{"sent": n})
}

// webPush sends a message to the browsers of one user (0 = all users); returns the number sent.
func (s *Server) webPush(ctx context.Context, m notify.Message, userID int64) int {
	subs, err := s.store.PushSubscriptions(ctx, userID)
	if err != nil || len(subs) == 0 {
		return 0
	}
	v, err := s.vapid(ctx)
	if err != nil {
		s.log.Warn("web push disabled", "err", err)
		return 0
	}
	payload, _ := json.Marshal(map[string]any{"title": m.Title, "body": m.Text, "url": m.URL, "severity": m.Severity,
		"tag": "monitor-" + strconv.FormatInt(m.MonitorID, 10)})
	sent := 0
	for _, sub := range subs {
		var ns notify.Subscription
		ns.Endpoint, ns.Keys.P256dh, ns.Keys.Auth = sub.Endpoint, sub.P256dh, sub.Auth
		err := v.Push(ctx, s.pushClient, ns, payload, 24*time.Hour)
		switch {
		case errors.Is(err, notify.ErrGone):
			_ = s.store.DeletePushSubscription(ctx, sub.Endpoint)
		case err != nil:
			s.log.Warn("web push failed", "err", err)
			s.metrics.NotifyFailed("webpush")
		default:
			sent++
		}
	}
	return sent
}
