package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
	"github.com/openbrandexchange/obx-go/internal/safedial"
)

// checkServerOptions configure the HTTP form of the online check, as run in
// the container behind the compatibility checker on openbrandexchange.com.
type checkServerOptions struct {
	Online        conformance.Online
	Timeout       time.Duration // per HTTP request
	Total         time.Duration // per check
	MaxConcurrent int
	Logger        *log.Logger
	// Resolver looks up the portal host before the check (tests).
	Resolver interface {
		LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
	}
}

// checkRequest is the body of POST /check. The client secret only lives in
// memory for the duration of the check; it is never logged and is redacted
// from the report.
type checkRequest struct {
	URL    string `json:"url"`
	Client *struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	} `json:"client,omitempty"`
}

// checkError is an answer for a check that did not start.
type checkError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *checkError) Error() string { return e.Message }

// runCheckServer serves POST /check: {"url": "https://portal", "client":
// {"id": "…", "secret": "…"}} returns the report as JSON, or with
// "Accept: application/x-ndjson" one line per finished test and then the
// report (see streamLine).
func runCheckServer(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("check-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8080", "listen address")
	var o checkServerOptions
	fs.IntVar(&o.Online.MaxRequests, "max-requests", 800, "maximum HTTP requests per check")
	maxMiB := fs.Int64("max-mib", 64, "maximum MiB of responses per check")
	fs.DurationVar(&o.Online.MinInterval, "min-interval", 100*time.Millisecond, "minimum time between two requests of a check")
	fs.DurationVar(&o.Timeout, "timeout", 20*time.Second, "timeout per HTTP request")
	fs.DurationVar(&o.Total, "total-timeout", 3*time.Minute, "timeout per check")
	fs.IntVar(&o.MaxConcurrent, "max-concurrent", 2, "checks at the same time; more answer 503")
	fs.BoolVar(&o.Online.AllowInsecure, "allow-insecure", false, "allow http and private addresses (local tests only)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	o.Online.MaxBytes = *maxMiB << 20
	o.Logger = log.New(stderr, "", log.LstdFlags|log.LUTC)
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "obx-conformance check-server: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "OBX online check listening on %s (POST /check)\n", ln.Addr())
	srv := &http.Server{Handler: newCheckServer(o), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "obx-conformance check-server: %v\n", err)
		return 2
	}
	return 0
}

func newCheckServer(o checkServerOptions) http.Handler {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 2
	}
	if o.Total <= 0 {
		o.Total = 3 * time.Minute
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Logger == nil {
		o.Logger = log.New(io.Discard, "", 0)
	}
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	slots := make(chan struct{}, o.MaxConcurrent)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("POST /check", func(w http.ResponseWriter, r *http.Request) {
		var req checkRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeCheckError(w, http.StatusBadRequest, &checkError{"bad_request", "the request body is not valid JSON"})
			return
		}
		base, host, cerr := checkTarget(r.Context(), req.URL, o.Online.AllowInsecure, o.Resolver)
		if cerr != nil {
			o.Logger.Printf("check refused code=%s", cerr.Code)
			writeCheckError(w, http.StatusBadRequest, cerr)
			return
		}
		cfg := conformance.Config{BaseURL: base, HTTPClient: &http.Client{Timeout: o.Timeout}}
		if req.Client != nil && (req.Client.ID != "" || req.Client.Secret != "") {
			if req.Client.ID == "" || req.Client.Secret == "" || len(req.Client.ID) > 256 || len(req.Client.Secret) > 512 {
				writeCheckError(w, http.StatusBadRequest, &checkError{"bad_client", "give both the client id and the client secret of the test API key"})
				return
			}
			cfg.ClientID, cfg.ClientSecret = req.Client.ID, req.Client.Secret
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			writeCheckError(w, http.StatusServiceUnavailable, &checkError{"busy", "the checker is busy; try again in a few minutes"})
			return
		}
		online := o.Online
		cfg.Online = &online
		ctx, cancel := context.WithTimeout(r.Context(), o.Total)
		defer cancel()

		var lines *json.Encoder
		flusher, _ := w.(http.Flusher)
		if strings.Contains(r.Header.Get("Accept"), "application/x-ndjson") {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			lines = json.NewEncoder(w)
			cfg.Progress = func(res conformance.Result) {
				lines.Encode(streamLine{Type: "result", Result: &res})
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		start := time.Now()
		rep, err := conformance.Run(ctx, []string{conformance.ProfileProtocol}, cfg)
		// Only metadata is logged: never the key, the secret or a token.
		if err != nil {
			o.Logger.Printf("check host=%s error=%q", host, err)
			if lines != nil {
				lines.Encode(streamLine{Type: "error", Error: &checkError{"internal", "the check could not run"}})
				return
			}
			writeCheckError(w, http.StatusInternalServerError, &checkError{"internal", "the check could not run"})
			return
		}
		o.Logger.Printf("check host=%s key=%t duration=%s passed=%d failed=%d skipped=%d requests=%d bytes=%d setup_error=%t",
			host, cfg.ClientID != "", time.Since(start).Round(time.Millisecond), rep.Passed, rep.Failed, rep.Skipped, rep.Requests, rep.Bytes, rep.SetupError != "")
		if lines != nil {
			lines.Encode(streamLine{Type: "report", Report: rep})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(rep)
	})
	return mux
}

func writeCheckError(w http.ResponseWriter, status int, e *checkError) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Error *checkError `json:"error"`
	}{e})
}

// checkTarget validates the portal address of a check: https on the default
// port, a host name (no IP address, no user info) that resolves to public
// addresses only. The connection itself is checked again at dial time.
func checkTarget(ctx context.Context, raw string, allowInsecure bool, res interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}) (base, host string, _ *checkError) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2048 {
		return "", "", &checkError{"invalid_url", "the address is too long"}
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", &checkError{"invalid_url", "this is not a valid web address"}
	}
	if u.Scheme != "https" && !(allowInsecure && u.Scheme == "http") {
		return "", "", &checkError{"not_https", "the portal address must start with https://"}
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", &checkError{"invalid_url", "give the portal address without a user name, query or fragment"}
	}
	host = strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "443" && !allowInsecure {
		return "", "", &checkError{"port", "the online check only connects to port 443"}
	}
	if net.ParseIP(host) != nil && !allowInsecure {
		return "", "", &checkError{"ip_address", "give the portal's domain name, not an IP address"}
	}
	if !allowInsecure {
		if !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
			return "", "", &checkError{"not_public", "the portal address is not a public internet address"}
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addrs, err := res.LookupIPAddr(lctx, host)
		if err != nil || len(addrs) == 0 {
			return "", "", &checkError{"dns", "the portal's domain name does not resolve"}
		}
		for _, a := range addrs {
			if why := safedial.Reason(a.IP, false); why != "" {
				return "", "", &checkError{"not_public", "the portal address is not a public internet address (it resolves to a " + why + " address)"}
			}
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	if u.Path == "/.well-known/obx" {
		u.Path = ""
	}
	return u.Scheme + "://" + u.Host + u.Path, host, nil
}
