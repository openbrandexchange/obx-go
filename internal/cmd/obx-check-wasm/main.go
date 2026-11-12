//go:build js && wasm

// Command obx-check-wasm is a size and feasibility prototype: the read-only
// online check (conformance.Online) compiled to WebAssembly for a
// JavaScript host such as a Cloudflare Worker. It exports
// obxCheck(url, clientID, clientSecret) → Promise<report JSON>. HTTP goes
// through the host's fetch (net/http's js transport), so the SSRF checks of
// internal/safedial do not apply here and must be done by the host.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"syscall/js"
	"time"

	"github.com/openbrandexchange/obx-go/conformance"
)

func main() {
	js.Global().Set("obxCheck", js.FuncOf(func(this js.Value, args []js.Value) any {
		url, id, secret := args[0].String(), "", ""
		if len(args) > 2 {
			id, secret = args[1].String(), args[2].String()
		}
		insecure := len(args) > 3 && args[3].Bool() // local measurements only
		handler := js.FuncOf(func(this js.Value, p []js.Value) any {
			resolve, reject := p[0], p[1]
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				rep, err := conformance.Run(ctx, []string{conformance.ProfileProtocol}, conformance.Config{
					BaseURL: url, ClientID: id, ClientSecret: secret,
					HTTPClient: &http.Client{Transport: http.DefaultTransport, Timeout: 20 * time.Second},
					Online:     &conformance.Online{AllowInsecure: insecure},
				})
				if err != nil {
					reject.Invoke(err.Error())
					return
				}
				b, _ := json.Marshal(rep)
				resolve.Invoke(string(b))
			}()
			return nil
		})
		return js.Global().Get("Promise").New(handler)
	}))
	select {}
}
