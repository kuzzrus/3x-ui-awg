package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Server answers the master. Every request needs the pairing secret, and a wrong
// path, method or secret all get the same bare 404, so a response never says which.
type Server struct {
	core     *Core
	secret   string
	guid     string
	geoDir   string
	updater  *updater
	sys      sysSampler
	routes   map[string]http.HandlerFunc
	paths    map[string]bool
	refusals refusalLog
}

func NewServer(core *Core, state *State, secret string) (*Server, error) {
	guid, err := state.Guid()
	if err != nil {
		return nil, err
	}
	// The core reads its geo files from the folder it is started from.
	s := &Server{core: core, secret: secret, guid: guid, geoDir: config.GetBinFolderPath(), updater: &updater{stateDir: state.Dir()}}
	s.routes = map[string]http.HandlerFunc{}
	s.paths = map[string]bool{}
	for _, route := range []struct {
		method, path string
		handler      http.HandlerFunc
	}{
		{http.MethodPut, agentproto.PathConfig, s.putConfig},
		{http.MethodGet, agentproto.PathStatus, s.getStatus},
		{http.MethodGet, agentproto.PathStats, s.getStats},
		{http.MethodPost, agentproto.PathRestart, s.postRestart},
		{http.MethodGet, agentproto.PathGeo, s.getGeo},
		{http.MethodPut, agentproto.PathGeo, s.putGeo},
		{http.MethodPost, agentproto.PathUpdate, s.postUpdate},
		{http.MethodGet, agentproto.PathUpdate, s.getUpdate},
	} {
		s.routes[route.method+" "+route.path] = route.handler
		s.paths[route.path] = true
	}
	// The first sample only sets the baseline the later ones are measured from.
	s.sys.sample()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler, found := s.routes[r.Method+" "+r.URL.Path]
	authorization := r.Header.Get("Authorization")
	var reason string
	switch {
	case !found && s.paths[r.URL.Path]:
		reason = "wrong method"
	case !found:
		reason = "unknown path"
	case authorization == "":
		reason = "no secret"
	case !agentproto.CheckBearer(authorization, s.secret):
		reason = "wrong secret"
	}
	if reason != "" {
		s.refusals.note(r, reason)
		http.NotFound(w, r)
		return
	}
	handler(w, r)
}

// Serve answers on ln until ctx ends; tlsConfig carries the certificate and the SNI gate.
func (s *Server) Serve(ctx context.Context, ln net.Listener, tlsConfig *tls.Config) error {
	srv := &http.Server{
		Handler:           s,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(debugWriter{}, "", 0),
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = srv.Shutdown(shutdown)
	}()

	err := srv.ServeTLS(ln, "", "")
	cancel()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// debugWriter sends the http server's own complaints, mostly handshakes that scanners
// abandon, to the debug log.
type debugWriter struct{}

func (debugWriter) Write(p []byte) (int, error) {
	logger.Debug("agent: http:", string(p))
	return len(p), nil
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	restart := false
	if value := r.URL.Query().Get(agentproto.QueryRestartOnUserRemoval); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be true or false", agentproto.QueryRestartOnUserRemoval))
			return
		}
		restart = parsed
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentproto.MaxConfigBytes))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, err.Error())
		return
	}
	resp, err := s.core.Apply(r.Context(), body, restart)
	replyApplied(w, resp, err)
}

func (s *Server) postRestart(w http.ResponseWriter, r *http.Request) {
	err := s.core.Restart(r.Context())
	// No config came with this request, so a core that will not come back is the agent's
	// failure, and 422 is left to mean a refused push.
	var refused *ConfigError
	if errors.As(err, &refused) {
		err = errors.New(refused.Reason)
	}
	snap := s.core.Snapshot()
	replyApplied(w, agentproto.ConfigResponse{
		Revision:  snap.Revision,
		Applied:   agentproto.AppliedRestart,
		XrayState: snap.XrayState,
	}, err)
}

func replyApplied(w http.ResponseWriter, resp agentproto.ConfigResponse, err error) {
	var refused *ConfigError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, resp)
	case errors.As(err, &refused):
		writeError(w, http.StatusUnprocessableEntity, refused.Reason)
	case errors.Is(err, ErrNoConfig):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) getStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.core.Snapshot()
	sys := s.sys.sample()
	hostname, _ := os.Hostname()
	writeJSON(w, http.StatusOK, agentproto.Status{
		AgentVersion:   config.GetPanelVersion(),
		Hostname:       hostname,
		Guid:           s.guid,
		ConfigRevision: snap.Revision,
		XrayVersion:    snap.XrayVersion,
		XrayState:      snap.XrayState,
		XrayError:      snap.XrayError,
		CpuPct:         sys.cpu,
		MemPct:         sys.mem,
		UptimeSecs:     sys.uptime,
		NetUp:          sys.netUp,
		NetDown:        sys.netDown,
	})
}

func (s *Server) getStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.core.Stats()
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, stats)
	case errors.Is(err, ErrCoreRestarted):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusBadGateway, "the core did not answer: "+err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, agentproto.ErrorBody{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
