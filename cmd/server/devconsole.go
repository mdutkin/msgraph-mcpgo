//go:build devconsole

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/config"
	"github.com/rs/zerolog"
)

// registerDevConsole mounts the browser test console and the Microsoft Entra
// device-code proxy that backs it.
//
// Build with: go build -tags devconsole ./cmd/server
//
// These routes are unauthenticated by necessity, because their purpose is to
// obtain the first token. The proxy exists only to avoid a browser CORS
// failure when calling Microsoft Entra directly from the console page.
//
// Never expose them outside a developer machine. An open device-code proxy for
// a confidential client is a phishing instrument: a caller starts a device
// code flow for this application's client ID, persuades an employee to approve
// the code, and then polls the same proxy to collect a delegated token for
// that employee's mailbox, calendar, chats and files.
func registerDevConsole(mux *http.ServeMux, cfg *config.Config, logger *zerolog.Logger) {
	logger.Warn().
		Msg("DEVELOPMENT CONSOLE ENABLED: /test, /auth/devicecode and /auth/token are unauthenticated. Never run this build outside a developer machine.")

	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/test.html")
	})

	proxy := &devAuthProxy{
		tenantID: cfg.AzureTenantID,
		clientID: devConsoleClientID,
		client:   &http.Client{Timeout: 30 * time.Second},
		logger:   logger,
	}
	mux.HandleFunc("/auth/devicecode", proxy.deviceCode)
	mux.HandleFunc("/auth/token", proxy.token)
}

// devConsoleClientID is the Microsoft-published "Microsoft Azure CLI" public
// client. The device-code flow needs a client ID, and this server no longer has
// one of its own because it holds no credential. Using a well-known public
// client keeps the development console working without inventing an app
// registration that would only exist to support it.
const devConsoleClientID = "04b07795-8ddb-461a-bbee-02f9e1bf7b46"

// devAuthProxy forwards device-code requests to Microsoft Entra.
type devAuthProxy struct {
	tenantID string
	clientID string
	client   *http.Client
	logger   *zerolog.Logger
}

func (p *devAuthProxy) deviceCode(w http.ResponseWriter, r *http.Request) {
	// The console acquires a Microsoft Graph token directly, which is the
	// same kind of credential LibreChat forwards in production.
	scopes := r.FormValue("scope")
	if scopes == "" {
		scopes = "https://graph.microsoft.com/.default offline_access"
	}

	p.forward(w, r, "devicecode", url.Values{
		"client_id": {p.clientID},
		"scope":     {scopes},
	})
}

func (p *devAuthProxy) token(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.FormValue("device_code")
	if deviceCode == "" {
		http.Error(w, "device_code required", http.StatusBadRequest)
		return
	}

	p.forward(w, r, "token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {p.clientID},
		"device_code": {deviceCode},
	})
}

func (p *devAuthProxy) forward(w http.ResponseWriter, r *http.Request, endpoint string, form url.Values) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	target := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/%s", p.tenantID, endpoint)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Error().Err(err).Str("endpoint", endpoint).Msg("Device-code proxy request failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	// The response carries a token. It is streamed to the browser and never
	// logged.
	_, _ = io.Copy(w, resp.Body)
}
