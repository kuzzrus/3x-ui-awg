package agent

import (
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// A geo file is tens of megabytes, and the server's own timeouts are sized for a config push.
	geoUploadTimeout = 15 * time.Minute
	// What gzip may add to a file it cannot shrink, so the cap on the wire never refuses a file
	// the cap on the stored size would take.
	geoWireSlack = 1 << 20
)

// maxGeoBytes is a variable so a test can shrink it.
var maxGeoBytes int64 = agentproto.MaxGeoBytes

// getGeo lists the geo files the core reads, so the master can send the ones its config needs.
func (s *Server) getGeo(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.geoDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files := []agentproto.GeoFile{}
	for _, entry := range entries {
		if !agentproto.ValidGeoName(entry.Name()) {
			continue
		}
		// Stat, not the entry: a file linked in from a shared asset folder counts as held.
		info, err := os.Stat(filepath.Join(s.geoDir, entry.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, agentproto.GeoFile{Name: entry.Name(), Size: info.Size()})
	}
	writeJSON(w, http.StatusOK, agentproto.GeoFiles{Files: files})
}

// putGeo stores a geo file under the name the master gave it, once the bytes read match the
// SHA-256 it sent. The file is swapped in whole, so the core never reads half of one.
func (s *Server) putGeo(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	name := query.Get(agentproto.QueryGeoName)
	if !agentproto.ValidGeoName(name) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a geo file name such as geoip.dat", agentproto.QueryGeoName))
		return
	}
	want, err := hex.DecodeString(query.Get(agentproto.QueryGeoSha256))
	if err != nil || len(want) != sha256.Size {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be the 64 hex digits of the file's SHA-256", agentproto.QueryGeoSha256))
		return
	}

	control := http.NewResponseController(w)
	deadline := time.Now().Add(geoUploadTimeout)
	_ = control.SetReadDeadline(deadline)
	_ = control.SetWriteDeadline(deadline.Add(time.Minute))

	var src io.Reader = http.MaxBytesReader(w, r.Body, maxGeoBytes+geoWireSlack)
	switch encoding := r.Header.Get("Content-Encoding"); encoding {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(src)
		if err != nil {
			writeError(w, http.StatusBadRequest, "the body is not gzip: "+err.Error())
			return
		}
		defer zr.Close()
		src = zr
	default:
		writeError(w, http.StatusUnsupportedMediaType, "the agent does not read a body encoded as "+encoding)
		return
	}

	tmp, err := os.CreateTemp(s.geoDir, ".geo-*.tmp")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// After the rename there is nothing left at the temporary path, and these do nothing.
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	sink := &geoSink{file: tmp, hash: sha256.New()}
	copied, err := io.Copy(sink, io.LimitReader(src, maxGeoBytes+1))
	var writeFailed *sinkError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &writeFailed):
		writeError(w, http.StatusInternalServerError, "store the file: "+writeFailed.Error())
		return
	case errors.As(err, &tooLarge) || copied > maxGeoBytes:
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a geo file is at most %d bytes", maxGeoBytes))
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "read the body: "+err.Error())
		return
	case copied == 0:
		writeError(w, http.StatusBadRequest, "the body is empty")
		return
	}
	if subtle.ConstantTimeCompare(sink.hash.Sum(nil), want) != 1 {
		writeError(w, http.StatusBadRequest, "the body does not match its sha256")
		return
	}

	if err := tmp.Chmod(0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.geoDir, name)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logger.Infof("agent: stored geo file %s (%d bytes)", name, copied)
	writeJSON(w, http.StatusOK, agentproto.GeoFile{Name: name, Size: copied})
}

// sinkError marks a failure to write, which io.Copy would otherwise not tell from one to read.
type sinkError struct{ error }

// geoSink writes to the temporary file and the digest alike.
type geoSink struct {
	file *os.File
	hash hash.Hash
}

func (g *geoSink) Write(p []byte) (int, error) {
	n, err := g.file.Write(p)
	if err != nil {
		return n, &sinkError{err}
	}
	g.hash.Write(p[:n])
	return n, nil
}
