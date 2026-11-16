package agent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// A site is a few megabytes, but a slow link and the server's timeouts, which are sized for a config push, make an upload slow.
	siteUploadTimeout = 15 * time.Minute
	// siteKeepFor is how long a site that nothing names, and what an interrupted upload left, stays before it is swept, so
	// that the sweep does not take a site between the master sending it and naming it.
	siteKeepFor = 15 * time.Minute
)

// maxSiteBytes is a variable so a test can shrink it.
var maxSiteBytes int64 = agentproto.MaxDecoySiteBytes

// sitePath is the folder a site is unpacked into. The name is the SHA-256 of its zip, so a site is never changed once it is there.
func (h *decoyHost) sitePath(site string) string { return filepath.Join(h.sites, site) }

// holdsSite reports whether the site is unpacked whole.
func (h *decoyHost) holdsSite(site string) bool {
	return agentproto.ValidDecoySite(site) && frontproxy.DecoyInstalled(h.sitePath(site))
}

// Sites are the sites the agent holds whole, which the master reads from the stats to know whether to send one.
func (h *decoyHost) Sites() []string {
	names := []string{}
	entries, err := os.ReadDir(h.sites)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if entry.IsDir() && h.holdsSite(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

// StoreSite reads the zip of a site from src, checks it against the SHA-256 the master named, and unpacks it into a folder of
// its own, so that the site being served is never touched. A site the agent holds already is left as it is.
func (h *decoyHost) StoreSite(want string, src io.Reader) (agentproto.DecoySite, error) {
	if err := os.MkdirAll(h.sites, stateDirMode); err != nil {
		return agentproto.DecoySite{}, err
	}
	tmp, err := os.CreateTemp(h.sites, ".site-*.zip")
	if err != nil {
		return agentproto.DecoySite{}, err
	}
	// After the unpacking there is nothing left to keep of the zip.
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, sum), src)
	switch {
	case err != nil:
		return agentproto.DecoySite{}, err
	case size == 0:
		return agentproto.DecoySite{}, &agentproto.DecoyError{Reason: "the body is empty"}
	case subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum.Sum(nil))), []byte(want)) != 1:
		return agentproto.DecoySite{}, &agentproto.DecoyError{Reason: "the body does not match its sha256"}
	}

	h.siteMu.Lock()
	defer h.siteMu.Unlock()
	if !h.holdsSite(want) {
		if err := frontproxy.InstallDecoyArchive(h.sitePath(want), tmp, size); err != nil {
			var disk *fs.PathError
			if errors.As(err, &disk) {
				return agentproto.DecoySite{}, err
			}
			return agentproto.DecoySite{}, &agentproto.DecoyError{Reason: err.Error()}
		}
		logger.Infof("agent: stored the decoy site %s (%d bytes)", want, size)
	}
	// The decoy that waited for it need not wait for the next look.
	select {
	case h.wake <- struct{}{}:
	default:
	}
	return agentproto.DecoySite{Sha256: want, Size: size}, nil
}

// pruneSites removes what no decoy names any more: the sites other than the one wanted and the one served, and what an
// interrupted upload left, each once it is older than siteKeepFor.
func (h *decoyHost) pruneSites() {
	h.mu.Lock()
	keep := []string{h.spec.Site}
	if h.applied != nil {
		keep = append(keep, h.applied.Site)
	}
	h.mu.Unlock()

	entries, err := os.ReadDir(h.sites)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if slices.Contains(keep, entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || h.now().Sub(info.ModTime()) < siteKeepFor {
			continue
		}
		if err := os.RemoveAll(filepath.Join(h.sites, entry.Name())); err != nil {
			logger.Warningf("agent: removing the decoy site %s: %v", entry.Name(), err)
		}
	}
}

// putDecoySite stores the zip of a decoy site under the SHA-256 the master named in the query.
func (s *Server) putDecoySite(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get(agentproto.QueryDecoySiteSha256)
	if !agentproto.ValidDecoySite(want) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be the 64 lower-case hex digits of the zip's SHA-256", agentproto.QueryDecoySiteSha256))
		return
	}
	control := http.NewResponseController(w)
	deadline := time.Now().Add(siteUploadTimeout)
	_ = control.SetReadDeadline(deadline)
	_ = control.SetWriteDeadline(deadline.Add(time.Minute))

	stored, err := s.decoy.StoreSite(want, http.MaxBytesReader(w, r.Body, maxSiteBytes))
	var tooLarge *http.MaxBytesError
	var refused *agentproto.DecoyError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, stored)
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a decoy site is at most %d bytes", maxSiteBytes))
	case errors.As(err, &refused):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store the site: "+err.Error())
	}
}
