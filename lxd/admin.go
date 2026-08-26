//go:build with_lxd

package lxd

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	C "github.com/sagernet/sing-box/constant"
)

const applyBodyLimit = 32 << 20

// labelBodyLimit caps a client-label write. The body is one short name; the
// limit exists so a stray large POST cannot be buffered.
const labelBodyLimit = 4 << 10

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
	mux.HandleFunc("GET /admin/info", c.handleInfo)
	// Resource plane (SPEC 063): the second admin payload beside the config —
	// files the config REFERS to (.srs rule-sets, geo bases). Client-facing, so
	// registered on the pinned mux, NOT the operator loopback path. Registered
	// only when a resource store is wired.
	if c.resources != nil {
		mux.HandleFunc("GET /admin/resources", c.handleResourceList)
		mux.HandleFunc("PUT /admin/resources/{name}", c.handleResourcePut)
		mux.HandleFunc("GET /admin/resources/{name}", c.handleResourceStat)
		mux.HandleFunc("GET /admin/resources/{name}/content", c.handleResourceContent)
		mux.HandleFunc("DELETE /admin/resources/{name}", c.handleResourceDelete)
	}
	// Observability plane (SPEC 065): the daemon's own diagnostics — memory,
	// core traffic, its log, and pprof. Client-facing like the resource plane,
	// NOT operator loopback: the whole point is reaching them REMOTELY, from
	// the launcher, when a server misbehaves. An operator sitting on the host
	// already has kill -QUIT and a local profiler.
	mux.HandleFunc("GET /admin/memory", c.handleMemory)
	mux.HandleFunc("GET /admin/stats", c.handleStats)
	mux.HandleFunc("GET /admin/logs", c.handleLogs)
	c.pprofHandler(mux)
	// Client identity directory (SPEC 066): IP → device, so the connection
	// inspector can label traffic with a device name instead of a bare address.
	// Client-facing for the same reason as the rest of this plane — the answer
	// is needed by the launcher UI, remotely. Registered only when wired.
	if c.clientInfo != nil {
		mux.HandleFunc("GET /admin/clients-info", c.handleClientsInfo)
		mux.HandleFunc("PUT /admin/clients-info/labels/{key}", c.handleClientLabelPut)
		mux.HandleFunc("DELETE /admin/clients-info/labels/{key}", c.handleClientLabelDelete)
	}
	// Host telemetry (SPEC 068): the machine the daemon runs ON, as opposed to
	// /admin/memory, which describes the process. Same plane, same pin — an
	// operator diagnosing "the router is slow" needs this remotely.
	if c.host != nil {
		mux.HandleFunc("GET /admin/host", c.handleHost)
		mux.HandleFunc("GET /admin/host/interfaces", c.handleHostInterfaces)
	}
	// Operator routes serve the `client` CLI over loopback: they need the
	// Bearer secret but NOT a client cert (the operator on the host has none).
	// Registered on the loopback-only, secret-gated, cert-exempt path below.
	operator := http.NewServeMux()
	if c.clients != nil {
		operator.HandleFunc("GET /admin/clients", c.handleClients)
		operator.HandleFunc("POST /admin/client-code", c.handleClientCode)
		operator.HandleFunc("POST /admin/client-remove", c.handleClientRemove)
	}

	var handler http.Handler = mux

	// mTLS pin: every route except enroll and the operator paths requires a
	// trusted client cert. The TLS layer only requests the cert (so enroll
	// works pre-trust); trust is enforced here, per request, against the
	// pinned registry. A trusted certificate is the client's FULL credential:
	// no Bearer on top (SPEC 057 revision — the secret is the daemon-owned
	// operator credential and clients never learn it, so requiring both would
	// lock every launcher out).
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
	} else {
		// Plain h2c (dev): the Bearer secret is the only gate, as before.
		// Constant-time — unlike the upstream gRPC interceptor.
		handler = withBearer(secret, handler)
	}

	if c.clients == nil {
		return handler
	}

	// Operator routes skip the cert pin, so they MUST stay unreachable from the
	// network: minting an enrollment code is trust-granting, and a cert-exempt,
	// remotely reachable mint would void the whole mTLS model (enroll must stay
	// the only route to trust). Loopback + secret is the operator contract.
	operatorHandler := requireLoopback(withBearer(secret, operator))

	// Root demux: enroll (code-guarded), operator routes (loopback + secret),
	// everything else (pin + secret).
	root := http.NewServeMux()
	root.HandleFunc("POST /admin/enroll", c.handleEnroll)
	root.Handle("/admin/clients", operatorHandler)
	root.Handle("/admin/client-code", operatorHandler)
	root.Handle("/admin/client-remove", operatorHandler)
	root.Handle("/", handler)
	return root
}

// withBearer wraps next behind a constant-time Bearer check; an empty secret
// disables the gate (loopback/dev mode by design — see FEATURE 014 §2).
func withBearer(secret string, next http.Handler) http.Handler {
	if secret == "" {
		return next
	}
	expected := []byte("Bearer " + secret)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		provided := []byte(request.Header.Get("Authorization"))
		if len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			writeJSON(writer, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// requireLoopback rejects requests whose peer is not a loopback address.
// RemoteAddr is set by the server from the socket, not from headers, so it
// cannot be spoofed by a remote client.
func requireLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			host = request.RemoteAddr
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.IsLoopback() {
			writeJSON(writer, http.StatusForbidden, map[string]any{"error": "operator routes are loopback-only"})
			return
		}
		next.ServeHTTP(writer, request)
	})
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

// handleClientCode mints a one-time enrollment invite for `client add`. The
// optional name is the operator's label for the future client: it is recorded
// with the code and wins over whatever name the enrolling client suggests.
func (c *controller) handleClientCode(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&body)
	code, err := c.clients.mintCode(body.Name)
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

// handleInfo serves the daemon's identity card: version, home directory,
// listen address, TLS mode, server fingerprint, uptime. Clients (the launcher)
// use it instead of hard-coding the daemon's paths on their side — e.g. the
// state_dir is where a client should point config paths the core WRITES
// (cache_file), because it is the one directory the daemon owns.
func (c *controller) handleInfo(writer http.ResponseWriter, request *http.Request) {
	logPath := ""
	if c.infoLogPath != "" {
		if _, err := os.Stat(c.infoLogPath); err == nil {
			logPath = c.infoLogPath
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"version":        C.Version,
		"state_dir":      c.infoStateDir,
		"listen":         c.advertiseAddr,
		"tls":            c.infoTLS,
		"fingerprint":    c.serverFingerprint,
		"pid":            os.Getpid(),
		"uptime_seconds": int(time.Since(c.startedAt).Seconds()),
		"log_path":       logPath,
	})
}

const resourceBodyLimit = 64 << 20 // .srs / geo bases run larger than a config

// withPath stamps the absolute path onto a metadata view: the store knows only
// the base name, the controller owns <state_dir>/resources. Absolute by owner
// decision (SPEC 063), matching /admin/info.state_dir, so the client copies it
// straight into a config's `type: local, path:`.
func (c *controller) withPath(meta resourceMeta) resourceMeta {
	meta.Path = filepath.Join(c.infoResourcesDir, meta.Name)
	return meta
}

func (c *controller) handleResourceList(writer http.ResponseWriter, request *http.Request) {
	metas, err := c.resources.List()
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	stamped := make([]resourceMeta, 0, len(metas))
	for _, meta := range metas {
		stamped = append(stamped, c.withPath(meta))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"resources": stamped})
}

func (c *controller) handleResourcePut(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	// Reference guard (B2) and the write itself are serialized under applyAccess
	// with apply: an apply that adds a reference cannot slip between the check
	// and the overwrite.
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	if c.closed {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "daemon is shutting down"})
		return
	}
	referenced, err := c.resourceReferenced(name)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if referenced {
		writeJSON(writer, http.StatusConflict, map[string]any{
			"error": "resource is referenced by the active or last-good config; apply a config without that reference first",
		})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, resourceBodyLimit))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	sha, size, err := c.resources.Put(name, content)
	if err != nil {
		// A bad name is the client's fault (400); anything else is disk (500).
		if isBadResourceName(err) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, c.withPath(resourceMeta{Name: name, SHA256: sha, Size: size}))
}

func (c *controller) handleResourceStat(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	meta, found, err := c.resources.Stat(name)
	if err != nil {
		if isBadResourceName(err) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such resource"})
		return
	}
	writeJSON(writer, http.StatusOK, c.withPath(meta))
}

func (c *controller) handleResourceContent(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	content, found, err := c.resources.Read(name)
	if err != nil {
		if isBadResourceName(err) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such resource"})
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	_, _ = writer.Write(content)
}

func (c *controller) handleResourceDelete(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	c.applyAccess.Lock()
	defer c.applyAccess.Unlock()
	if c.closed {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "daemon is shutting down"})
		return
	}
	referenced, err := c.resourceReferenced(name)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if referenced {
		writeJSON(writer, http.StatusConflict, map[string]any{
			"error": "resource is referenced by the active or last-good config; apply a config without that reference first",
		})
		return
	}
	found, err := c.resources.Delete(name)
	if err != nil {
		if isBadResourceName(err) {
			writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "no such resource"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"removed": true})
}

func (c *controller) handleMemory(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, c.memory.Snapshot())
}

// handleStats reports the CORE's counters. Uptime here is the core's, not the
// daemon's — the daemon's own is already in GET /admin/info.
//
// With no core up every field is null and the status stays 200: the endpoint
// describes the daemon, and "there is no core" is exactly the answer a client
// polls it for. A 503 would make it useless in the one state where the daemon
// is the only thing left to ask.
func (c *controller) handleStats(writer http.ResponseWriter, request *http.Request) {
	var snapshot statsSnapshot
	if source, ok := c.service.(statsSource); ok {
		if uptime, uplink, downlink, connections, live := source.CoreStats(); live {
			seconds := int64(uptime.Seconds())
			snapshot = statsSnapshot{
				CoreUptimeSeconds: &seconds,
				UplinkTotal:       &uplink,
				DownlinkTotal:     &downlink,
				Connections:       &connections,
			}
		}
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

// handleLogs serves the tail of the daemon's own log file — the one channel
// the gRPC log stream cannot carry (see logtail.go).
func (c *controller) handleLogs(writer http.ResponseWriter, request *http.Request) {
	path := c.infoLogPath
	if path == "" {
		writeJSON(writer, http.StatusNotFound, map[string]any{
			"error": "no log file (the daemon logs to the terminal when not run as a service)",
		})
		return
	}
	requested, _ := strconv.Atoi(request.URL.Query().Get("tail"))
	content, found, err := tailLog(path, clampTailLines(requested))
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !found {
		// Absent is a legitimate state, not a fault: in a terminal the daemon
		// leaves the log on the operator's screen and writes no file at all.
		writeJSON(writer, http.StatusNotFound, map[string]any{
			"error": "no log file at " + path + " (the daemon logs to the terminal when not run as a service)",
		})
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write([]byte(content))
}

// handleClientsInfo serves the IP → device directory (SPEC 066). It answers
// whether or not a core is up: the map is built from the host's own DHCP,
// neighbor and hostapd tables, not from the instance.
func (c *controller) handleClientsInfo(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, c.clientInfo.Snapshot())
}

// handleClientLabelPut records the operator's name for a client. The key is an
// IP or a MAC; a MAC label follows the device across addresses, an IP label is
// stabler for phones that randomize their MAC.
func (c *controller) handleClientLabelPut(writer http.ResponseWriter, request *http.Request) {
	key, ok := normalizeLabelKey(request.PathValue("key"))
	if !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "label key must be an IP address or a MAC address"})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, labelBodyLimit)).Decode(&body); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		// Clearing a label is DELETE; an empty name here is a malformed write,
		// not an implicit delete, so the client's intent is never guessed.
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "name must not be empty; use DELETE to clear a label"})
		return
	}
	if err := c.clientInfo.SetLabel(key, name); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"key": key, "name": name})
}

func (c *controller) handleClientLabelDelete(writer http.ResponseWriter, request *http.Request) {
	key, ok := normalizeLabelKey(request.PathValue("key"))
	if !ok {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "label key must be an IP address or a MAC address"})
		return
	}
	if err := c.clientInfo.DeleteLabel(key); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"removed": true})
}

// handleHost serves the host's own telemetry (SPEC 068) — CPU, memory,
// thermals, disks, descriptors. Like the rest of this plane it answers whether
// or not a core is up: it describes the machine, not the instance.
func (c *controller) handleHost(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, c.host.Host())
}

func (c *controller) handleHostInterfaces(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, c.host.Interfaces())
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
