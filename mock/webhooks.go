package mock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/internal/safedial"
	"github.com/openbrandexchange/obx-go/obx"
	"github.com/openbrandexchange/obx-go/webhook"
)

// webhookSub is a webhook subscription of an API client or of the brand
// admin (owner "admin").
type webhookSub struct {
	id, owner, url string
	events         []string
	secret         string
	status         string
	createdAt      time.Time
	lastDeliveryAt *time.Time
	lastStatus     *int
}

func (w *webhookSub) public(withSecret bool) obx.WebhookSubscription {
	s := obx.WebhookSubscription{ID: w.id, URL: w.url, Events: w.events, Status: w.status, CreatedAt: w.createdAt, LastDeliveryAt: w.lastDeliveryAt, LastStatus: w.lastStatus}
	if withSecret {
		s.Secret = ptr(w.secret)
	}
	return s
}

type webhookDelivery struct {
	ID         string    `json:"id"`
	EventID    string    `json:"event_id"`
	EventType  string    `json:"event_type"`
	At         time.Time `json:"at"`
	StatusCode *int      `json:"status_code"`
	DurationMS *int      `json:"duration_ms"`
	Attempt    int       `json:"attempt"`
	Success    bool      `json:"success"`
	Error      *string   `json:"error"`
}

// checkOutboundURL applies the SSRF rules of security.md section 5.
func (n *Node) checkOutboundURL(raw string, schemes ...string) *obx.Problem {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return problem(obx.ErrUnsafeURL, "not a valid web address").WithField("/url", obx.ErrUnsafeURL, "invalid URL")
	}
	allowed := append([]string{"https"}, schemes...)
	if n.opts.AllowPrivateURLs {
		allowed = append(allowed, "http")
	}
	if !obx.Contains(allowed, u.Scheme) {
		return problem(obx.ErrUnsafeURL, "the address must start with https://").WithField("/url", obx.ErrUnsafeURL, "https required")
	}
	host := u.Hostname()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addrs) == 0 {
			return problemf(obx.ErrUnsafeURL, "%s cannot be resolved", host).WithField("/url", obx.ErrUnsafeURL, "unresolvable host")
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if reason := safedial.Reason(ip, n.opts.AllowPrivateURLs); reason != "" {
			return problemf(obx.ErrUnsafeURL, "%s points to a %s address", host, reason).WithField("/url", obx.ErrUnsafeURL, reason+" address")
		}
	}
	return nil
}

// webhookHTTP returns the client used for deliveries and scheduled imports:
// no redirects, 10 s, and every connection checked on its resolved address
// (the address is pinned, so DNS rebinding cannot reach a private address).
func (n *Node) webhookHTTP() *http.Client {
	n.knobs.mu.Lock()
	c := n.knobs.webhookClient
	n.knobs.mu.Unlock()
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: safedial.Transport(n.opts.AllowPrivateURLs), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// send delivers one event synchronously (without holding n.mu) and returns
// the delivery record.
func (n *Node) send(sub webhookSub, eventType string, data any) webhookDelivery {
	evID := randomToken("evt_", 9)
	now := n.now()
	body := canonicalJSON(obx.WebhookEvent{ID: evID, Type: eventType, CreatedAt: now, Data: canonicalJSON(data)})
	d := webhookDelivery{ID: randomToken("dlv_", 6), EventID: evID, EventType: eventType, At: now, Attempt: 1}
	req, err := http.NewRequest(http.MethodPost, sub.url, bytes.NewReader(body))
	if err != nil {
		d.Error = ptr(err.Error())
		return d
	}
	for k, v := range webhook.Headers(evID, now, body, sub.secret) {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "obx-go-mock-webhooks")
	start := time.Now()
	resp, err := n.webhookHTTP().Do(req)
	d.DurationMS = ptr(int(time.Since(start).Milliseconds()))
	if err != nil {
		msg := err.Error()
		var ue *url.Error
		if errors.As(err, &ue) {
			msg = ue.Err.Error()
		}
		d.Error = &msg
		return d
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	d.StatusCode = ptr(resp.StatusCode)
	d.Success = resp.StatusCode >= 200 && resp.StatusCode < 300
	return d
}

// record stores a delivery result (holds n.mu).
func (n *Node) recordDelivery(subID string, d webhookDelivery) {
	s := n.st
	sub := s.webhooks[subID]
	if sub == nil {
		return
	}
	at := d.At
	sub.lastDeliveryAt = &at
	sub.lastStatus = d.StatusCode
	s.deliveries[subID] = append([]webhookDelivery{d}, s.deliveries[subID]...)
	if len(s.deliveries[subID]) > 100 {
		s.deliveries[subID] = s.deliveries[subID][:100]
	}
}

// dispatch sends an event to matching subscriptions in the background.
// Called with n.mu held.
func (n *Node) dispatch(match func(*webhookSub) bool, eventType string, data any) {
	for _, sub := range n.st.webhooks {
		if sub.status != "active" || !obx.Contains(sub.events, eventType) || !match(sub) {
			continue
		}
		cp := *sub
		go func() {
			d := n.send(cp, eventType, data)
			n.mu.Lock()
			n.recordDelivery(cp.id, d)
			n.mu.Unlock()
		}()
	}
}

func (n *Node) notifyChanges(seq int64) {
	n.dispatch(func(s *webhookSub) bool { return strings.HasPrefix(s.owner, "client:") }, obx.EventChangesAvailable, map[string]int64{"latest_seq": seq})
}

func (n *Node) notifyGrant(r *retailer) {
	g := n.st.grantOf(r)
	n.dispatch(func(s *webhookSub) bool {
		c := n.st.clients[strings.TrimPrefix(s.owner, "client:")]
		return c != nil && c.retailerID == r.id
	}, obx.EventGrantUpdated, map[string]any{"grant": g})
}

func (n *Node) notifyExport(clientID string, e *export) {
	n.dispatch(func(s *webhookSub) bool { return s.owner == "client:"+clientID }, obx.EventExportCompleted, map[string]string{"export_id": e.ID, "status": e.Status})
}

func (n *Node) notifyAdmin(eventType string, data any) {
	n.dispatch(func(s *webhookSub) bool { return s.owner == "admin" }, eventType, data)
}

// ---- protocol webhook endpoints

var consumerEvents = []string{obx.EventChangesAvailable, obx.EventGrantUpdated, obx.EventExportCompleted}

func (n *Node) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapWebhooks) {
		writeProblem(w, problem(obx.ErrNotImplemented, "webhooks are not offered"))
		return
	}
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	owner := "client:" + c.client.id
	if r.Method == http.MethodGet {
		out := obx.Items[obx.WebhookSubscription]{Items: []obx.WebhookSubscription{}}
		for _, s := range n.sortedSubs(owner) {
			out.Items = append(out.Items, s.public(false))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var in obx.WebhookInput
	if p := readJSON(r, &in); p != nil {
		writeProblem(w, p)
		return
	}
	sub, p := n.createSub(owner, in, consumerEvents, 5)
	if p != nil {
		writeProblem(w, p)
		return
	}
	writeJSON(w, http.StatusCreated, sub.public(true))
}

func (n *Node) sortedSubs(owner string) []*webhookSub {
	var out []*webhookSub
	for _, s := range n.st.webhooks {
		if s.owner == owner {
			out = append(out, s)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].id < out[j-1].id; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (n *Node) createSub(owner string, in obx.WebhookInput, allowedEvents []string, max int) (*webhookSub, *obx.Problem) {
	if len(in.Events) == 0 {
		return nil, problem(obx.ErrValidationFailed, "choose at least one event").WithField("/events", obx.ErrValidationFailed, "required")
	}
	for _, e := range in.Events {
		if !obx.Contains(allowedEvents, e) {
			return nil, problemf(obx.ErrValidationFailed, "unknown event %q", e).WithField("/events", obx.ErrValidationFailed, "unknown event")
		}
	}
	if len(n.sortedSubs(owner)) >= max {
		return nil, problemf(obx.ErrConflict, "at most %d webhooks", max)
	}
	if p := n.checkOutboundURL(in.URL); p != nil {
		return nil, p
	}
	sub := &webhookSub{id: n.st.newID("whk"), owner: owner, url: in.URL, events: in.Events, secret: randomToken(obx.PrefixWebhookSecret, 24), status: "active", createdAt: n.now()}
	n.st.webhooks[sub.id] = sub
	return sub, nil
}

func (n *Node) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.capability(obx.CapWebhooks) {
		writeProblem(w, problem(obx.ErrNotImplemented, "webhooks are not offered"))
		return
	}
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		return
	}
	sub := n.st.webhooks[r.PathValue("id")]
	if sub == nil || sub.owner != "client:"+c.client.id {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	delete(n.st.webhooks, sub.id)
	w.WriteHeader(http.StatusNoContent)
}

func (n *Node) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	if !n.capability(obx.CapWebhooks) {
		n.mu.Unlock()
		writeProblem(w, problem(obx.ErrNotImplemented, "webhooks are not offered"))
		return
	}
	c, ok := n.authProtocol(w, r, obx.ScopeCatalogRead)
	if !ok {
		n.mu.Unlock()
		return
	}
	sub := n.st.webhooks[r.PathValue("id")]
	if sub == nil || sub.owner != "client:"+c.client.id {
		n.mu.Unlock()
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	cp := *sub
	n.mu.Unlock()
	d := n.send(cp, obx.EventWebhookTest, map[string]any{})
	n.mu.Lock()
	n.recordDelivery(cp.id, d)
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, obx.WebhookTestResult{Delivered: d.Success, Status: d.StatusCode})
}

// ---- access requests

type accessRequest struct {
	id, tokenHash string
	in            obx.AccessRequestInput
	status        string
	createdAt     time.Time
	decidedAt     *time.Time
	reason        *string
	retailerID    string
	clientID      string // internal id of the created API client
	secret        string // client secret until claimed
	claimed       bool
	invited       bool
	language      string
}

func (a *accessRequest) access() []string {
	if len(a.in.Access) == 0 {
		return []string{"portal"}
	}
	return a.in.Access
}

func (n *Node) handleCreateAccessRequest(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if !n.capability(obx.CapAccessRequests) {
		writeProblem(w, problem(obx.ErrNotImplemented, "access requests are not offered"))
		return
	}
	if !s.setupDone {
		writeProblem(w, problem(obx.ErrSetupRequired, "this node has not been set up yet"))
		return
	}
	if !s.settings.accessRequests {
		writeProblem(w, problem(obx.ErrForbidden, "this brand does not accept access requests"))
		return
	}
	n.knobs.mu.Lock()
	perHour := n.knobs.accessRateLimit
	n.knobs.mu.Unlock()
	key := "access:" + clientIP(r)
	rw := s.rate[key]
	now := n.now()
	if rw == nil || now.Sub(rw.start) >= time.Hour {
		rw = &rateWindow{start: now}
		s.rate[key] = rw
	}
	rw.count++
	if rw.count > perHour {
		p := problem(obx.ErrRateLimited, "too many access requests from this address; try again later")
		ra := int(time.Hour.Seconds() - now.Sub(rw.start).Seconds())
		p.RetryAfter = &ra
		writeProblem(w, p)
		return
	}
	raw, p := readRaw(r)
	if p != nil {
		writeProblem(w, p)
		return
	}
	if p := validateSchema("AccessRequestInput", raw); p != nil {
		writeProblem(w, p)
		return
	}
	var in obx.AccessRequestInput
	if p := decodeRaw(raw, &in); p != nil {
		writeProblem(w, p)
		return
	}
	token := randomToken(obx.PrefixRequestToken, 24)
	ar := &accessRequest{id: s.newID("acr"), tokenHash: hashSecret(token), in: in, status: "pending", createdAt: now, language: requestLanguage(r, in.Contact.Language)}
	s.accessRequests[ar.id] = ar
	base := n.baseURL(r)
	for _, u := range s.users {
		if u.role == "admin" && u.status == "active" {
			n.email(u.email, "access_request", u.language, base+"/retailers/requests/"+ar.id, map[string]string{"retailer": in.Retailer.Name})
		}
	}
	n.notifyAdmin(obx.EventAccessRequestCreated, map[string]string{"access_request_id": ar.id, "retailer_name": in.Retailer.Name})
	out := n.accessRequestView(ar)
	out.RequestToken = token
	out.StatusURL = base + obx.PathAPIBase + "/access-requests/" + ar.id
	writeJSON(w, http.StatusAccepted, out)
}

func (n *Node) accessRequestView(ar *accessRequest) obx.AccessRequest {
	return obx.AccessRequest{ID: ar.id, Status: ar.status, CreatedAt: ar.createdAt, DecidedAt: ar.decidedAt, Reason: ar.reason}
}

func (n *Node) handleGetAccessRequest(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := n.st
	if !n.capability(obx.CapAccessRequests) {
		writeProblem(w, problem(obx.ErrNotImplemented, "access requests are not offered"))
		return
	}
	tok := bearer(r)
	if tok == "" {
		writeProblem(w, problem(obx.ErrUnauthorized, "send the request token as Bearer"))
		return
	}
	ar := s.accessRequests[r.PathValue("id")]
	if ar == nil || !constantEqual(ar.tokenHash, hashSecret(tok)) {
		writeProblem(w, problem(obx.ErrNotFound, ""))
		return
	}
	out := n.accessRequestView(ar)
	base := n.baseURL(r)
	if ar.status == "approved" {
		if obx.Contains(ar.access(), "portal") {
			out.Portal = &obx.AccessRequestPortal{Invited: ar.invited, LoginURL: base + "/login"}
		}
		if c := s.clients[ar.clientID]; c != nil {
			creds := &obx.AccessRequestCredentials{ClientID: c.clientID, TokenEndpoint: base + obx.PathAPIBase + "/oauth/token", Scopes: c.scopes, Claimed: ar.claimed}
			if !ar.claimed {
				creds.ClientSecret = ptr(ar.secret)
				ar.claimed, ar.secret = true, ""
			}
			out.Credentials = creds
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// requestLanguage picks the language for e-mails: the explicit choice, else
// the first Accept-Language tag.
func requestLanguage(r *http.Request, explicit *string) string {
	if explicit != nil && *explicit != "" {
		return *explicit
	}
	al := r.Header.Get("Accept-Language")
	if al != "" {
		tag := strings.TrimSpace(strings.Split(strings.Split(al, ",")[0], ";")[0])
		if tag != "" && tag != "*" {
			return strings.ToLower(strings.Split(tag, "-")[0])
		}
	}
	return "en"
}

// email records an e-mail in the outbox.
func (n *Node) email(to, kind, lang, link string, vars map[string]string) {
	subjects := map[string]string{
		"invite":          "You are invited to the brand portal",
		"access_request":  "New access request from %s",
		"access_approved": "Your access request was approved",
		"access_rejected": "Your access request",
		"zip_ready":       "Your download is ready",
		"password_reset":  "Reset your password",
	}
	subject := subjects[kind]
	if strings.Contains(subject, "%s") {
		subject = fmt.Sprintf(subject, vars["retailer"])
	}
	n.st.outbox = append(n.st.outbox, Email{To: to, Kind: kind, Language: lang, Subject: subject, Link: link, Body: subject + "\n\n" + link, At: n.now()})
}
