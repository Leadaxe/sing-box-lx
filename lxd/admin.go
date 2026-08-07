//go:build with_lx_command

package lxd

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const applyBodyLimit = 32 << 20

// adminHandler is the launcher-facing REST plane. It shares the listener with
// the gRPC plane and must work with a plain stdlib HTTP client (the launcher's
// win7 build cannot carry grpc-go). Bearer comparison is constant-time —
// unlike the upstream gRPC interceptor this plane does not inherit that flaw.
func (c *controller) adminHandler(secret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/apply", c.handleApply)
	mux.HandleFunc("POST /admin/rollback", c.handleRollback)
	mux.HandleFunc("POST /admin/start", c.handleStart)
	mux.HandleFunc("POST /admin/stop", c.handleStop)
	mux.HandleFunc("GET /admin/config", c.handleConfig)
	mux.HandleFunc("GET /admin/status", c.handleStatus)
	// Operator routes serve the `client` CLI over loopback: they need the
	// Bearer secret but NOT a client cert (the operator on the host has none).
	// Registered on the secret-gated but cert-exempt path below.
	operator := http.NewServeMux()
	if c.clients != nil {
		operator.HandleFunc("GET /admin/clients", c.handleClients)
		operator.HandleFunc("POST /admin/client-code", c.handleClientCode)
		operator.HandleFunc("POST /admin/client-remove", c.handleClientRemove)
	}

	var handler http.Handler = mux

	// mTLS pin: every route except enroll requires a trusted client cert. The
	// TLS layer only requests the cert (so enroll works pre-trust); trust is
	// enforced here, per request, against the pinned registry.
	if c.clients != nil {
		pinned := handler
		handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 ||
				!c.clients.isTrusted(fingerprintOf(request.TLS.PeerCertificates[0].Raw)) {
				writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "client certificate not trusted"})
				return
			}
			pinned.ServeHTTP(writer, request)
		})
	}

	// Bearer secret: a second factor layered over the pin (or the only factor
	// when TLS is off). Constant-time — unlike the upstream gRPC interceptor.
	if secret != "" {
		expected := []byte("Bearer " + secret)
		secretGated := handler
		handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			provided := []byte(request.Header.Get("Authorization"))
			if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
				writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
			secretGated.ServeHTTP(writer, request)
		})
	}

	if c.clients == nil {
		return handler
	}

	// Operator routes get the secret gate but skip the cert pin.
	var operatorHandler http.Handler = operator
	if secret != "" {
		expected := []byte("Bearer " + secret)
		gated := operatorHandler
		operatorHandler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			provided := []byte(request.Header.Get("Authorization"))
			if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
				writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
			gated.ServeHTTP(writer, request)
		})
	}

	// Root demux: enroll (code-guarded), operator routes (secret only),
	// everything else (pin + secret).
	root := http.NewServeMux()
	root.HandleFunc("POST /admin/enroll", c.handleEnroll)
	root.Handle("/admin/clients", operatorHandler)
	root.Handle("/admin/client-code", operatorHandler)
	root.Handle("/admin/client-remove", operatorHandler)
	root.Handle("/", handler)
	return root
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func (c *controller) handleApply(writer http.ResponseWriter, request *http.Request) {
	content, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, applyBodyLimit))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if strings.TrimSpace(string(content)) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "empty body"})
		return
	}
	result := c.Apply(request.Context(), string(content))
	switch result.Outcome {
	case applyApplied:
		response := map[string]any{"applied": true, "active_sha256": contentSHA(string(content))}
		if result.Err != nil {
			response["warning"] = result.Err.Error()
		}
		writeJSON(writer, http.StatusOK, response)
	case applyRejected:
		writeJSON(writer, http.StatusUnprocessableEntity, map[string]any{
			"applied": false, "rolled_back": false, "error": result.Err.Error(),
		})
	case applyError:
		// Infrastructure failure, not a config verdict: 500, not 422.
		writeJSON(writer, http.StatusInternalServerError, map[string]any{
			"applied": false, "rolled_back": false, "error": result.Err.Error(),
		})
	default:
		writeJSON(writer, http.StatusInternalServerError, map[string]any{
			"applied": false, "rolled_back": result.RolledBack, "error": result.Err.Error(),
		})
	}
}

func (c *controller) handleStart(writer http.ResponseWriter, request *http.Request) {
	result, found := c.Start(request.Context())
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no config to start from"})
		return
	}
	if result.Outcome != applyApplied {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"started": false, "error": result.Err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"started": true})
}

func (c *controller) handleStop(writer http.ResponseWriter, request *http.Request) {
	if err := c.Stop(); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"stopped": false, "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"stopped": true})
}

type enrollRequest struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	CertPEM string `json:"cert_pem"`
}

// handleEnroll pins a launcher's certificate against a one-time code. It is
// reachable before the client is trusted (guarded by the code alone) and runs
// over the server-authenticated TLS the launcher already validated by pinning
// the server fingerprint from the invite string.
func (c *controller) handleEnroll(writer http.ResponseWriter, request *http.Request) {
	var body enrollRequest
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	certDER, err := decodeCertPEM([]byte(body.CertPEM))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	name := body.Name
	if name == "" {
		name = "client"
	}
	client, err := c.clients.enroll(body.Code, name, certDER)
	if err != nil {
		writeJSON(writer, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"enrolled": true, "name": client.Name, "fingerprint": client.Fingerprint,
	})
}

func (c *controller) handleClients(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"clients": c.clients.list()})
}

// handleClientCode mints a one-time enrollment invite for `client add`.
func (c *controller) handleClientCode(writer http.ResponseWriter, request *http.Request) {
	code, err := c.clients.mintCode()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"invite": c.advertiseAddr + "#" + c.serverFingerprint + "#" + code,
	})
}

func (c *controller) handleClientRemove(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	removed, err := c.clients.remove(body.Target)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !removed {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such client"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"removed": true})
}

func (c *controller) handleRollback(writer http.ResponseWriter, request *http.Request) {
	result, found := c.Rollback(request.Context())
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no last-good config recorded"})
		return
	}
	if result.Outcome != applyApplied {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"applied": false, "error": result.Err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"applied": true})
}

func (c *controller) handleConfig(writer http.ResponseWriter, request *http.Request) {
	c.stateAccess.Lock()
	content := c.activeContent
	c.stateAccess.Unlock()
	if content == "" {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no config applied yet"})
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(content))
}

func (c *controller) handleStatus(writer http.ResponseWriter, request *http.Request) {
	c.stateAccess.Lock()
	defer c.stateAccess.Unlock()
	var status string
	switch {
	case c.running:
		status = "started"
	case c.activeSHA == "" && c.lastError == "":
		status = "idle"
	default:
		status = "fatal"
	}
	lastGoodSHA := ""
	if lastGood, loaded, _ := c.store.LoadLastGood(); loaded {
		lastGoodSHA = contentSHA(lastGood)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":            status,
		"active_sha256":     c.activeSHA,
		"last_good_sha256":  lastGoodSHA,
		"last_error":        c.lastError,
		"interrupted_apply": c.interruptedApply,
	})
}
