package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray/geodata"
)

const (
	// A transfer costs the master a hash, a compression and a stream, nothing like a core, and one
	// node's slow link must not hold the others back, so a few dozen at once is still small.
	agentGeoConcurrency = 16
	// A failed upload is tried again after agentRetryAfter, then after twice as long each time.
	agentGeoRetryMax = 10 * time.Minute
	// How often the same complaint about the same node is made again.
	agentGeoWarnEvery = time.Hour
)

// agentGeoSlots bounds the geo files in flight across all agents.
var agentGeoSlots = make(chan struct{}, agentGeoConcurrency)

// agentGeoFile is a geo file the master can send: its name on the agent and where it lies here.
type agentGeoFile struct{ name, path string }

// agentGeoFilesNeeded lists the geo files a config makes the core read, from its tokens (as the panel's
// validator parses them) and the geodata section; names that are no plain *.dat come back as rejected.
func agentGeoFilesNeeded(config []byte) (names, rejected []string) {
	var root any
	if json.Unmarshal(config, &root) != nil {
		return nil, nil
	}
	need := map[string]struct{}{}
	collectTokenFiles(root, need)
	var parsed struct {
		Geodata struct {
			Assets []struct {
				File string `json:"file"`
			} `json:"assets"`
		} `json:"geodata"`
	}
	if json.Unmarshal(config, &parsed) == nil {
		for _, asset := range parsed.Geodata.Assets {
			if asset.File != "" {
				need[asset.File] = struct{}{}
			}
		}
	}
	for name := range need {
		if agentproto.ValidGeoName(name) {
			names = append(names, name)
		} else {
			rejected = append(rejected, name)
		}
	}
	slices.Sort(names)
	slices.Sort(rejected)
	return names, rejected
}

// collectTokenFiles adds the database of every routing or DNS token among the strings of v. A string
// is tried as a domain token and as an ip token, since where it sits does not say which it is.
func collectTokenFiles(v any, into map[string]struct{}) {
	switch value := v.(type) {
	case string:
		for _, kind := range []geodata.GeoKind{geodata.KindSite, geodata.KindIP} {
			if ref, err := geodata.ParseReference(value, kind); err == nil && ref.File != "" {
				into[ref.File] = struct{}{}
			}
		}
	case []any:
		for _, item := range value {
			collectTokenFiles(item, into)
		}
	case map[string]any:
		for _, item := range value {
			collectTokenFiles(item, into)
		}
	}
}

// warnOften makes a complaint about a node, at most once an hour for the same one.
func (s *AgentSyncService) warnOften(nodeID int, key, format string, args ...any) {
	s.mu.Lock()
	st := s.stateFor(nodeID)
	if st.warned == nil {
		st.warned = map[string]time.Time{}
	}
	if at, ok := st.warned[key]; ok && time.Since(at) < agentGeoWarnEvery {
		s.mu.Unlock()
		return
	}
	st.warned[key] = time.Now()
	s.mu.Unlock()
	logger.Warningf(format, args...)
}

// geoFilesToSend lists the geo files the config needs that the agent does not hold and this panel
// does. An agent from before the geo endpoint answers 404, and its config is pushed as it always was.
func (s *AgentSyncService) geoFilesToSend(ctx context.Context, n *model.Node, client *runtime.AgentClient, config []byte) ([]agentGeoFile, error) {
	needed, rejected := agentGeoFilesNeeded(config)
	for _, name := range rejected {
		s.warnOften(n.Id, "rejected "+name, "agent %s: the config reads %q, which is no plain *.dat name, so the panel cannot send it: it has to be on the node already", n.Name, name)
	}
	if len(needed) == 0 {
		return nil, nil
	}
	held, err := client.Geo(ctx)
	var answer *runtime.AgentError
	switch {
	case errors.As(err, &answer) && answer.Status == http.StatusNotFound:
		return nil, nil
	case err != nil:
		return nil, err
	}
	have := make(map[string]bool, len(held.Files))
	for _, file := range held.Files {
		have[file.Name] = true
	}
	var missing []agentGeoFile
	for _, name := range needed {
		if have[name] {
			continue
		}
		path := filepath.Join(assetDir(), name)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > agentproto.MaxGeoBytes {
			s.warnOften(n.Id, "no copy "+name, "agent %s lacks the geo file %s, which this panel has no usable copy of either", n.Name, name)
			continue
		}
		missing = append(missing, agentGeoFile{name: name, path: path})
	}
	return missing, nil
}

// geoRetryDelay is the wait after the given number of failed uploads in a row.
func geoRetryDelay(failures int) time.Duration {
	delay := agentRetryAfter
	for i := 1; i < failures && delay < agentGeoRetryMax; i++ {
		delay *= 2
	}
	return min(delay, agentGeoRetryMax)
}

// sendGeo sends the files in the background, so the sync tick that waits for every node is not held
// by a slow link. The config goes out on the first tick after they are there; it could not go out
// before, as the core would refuse it for the files it lacks.
func (s *AgentSyncService) sendGeo(n *model.Node, client *runtime.AgentClient, files []agentGeoFile) {
	s.mu.Lock()
	st := s.stateFor(n.Id)
	if st.sendingGeo {
		s.mu.Unlock()
		return
	}
	st.sendingGeo = true
	s.mu.Unlock()

	go func() {
		err := sendGeoFiles(n.Name, client, files)
		s.mu.Lock()
		defer s.mu.Unlock()
		st.sendingGeo = false
		if err != nil {
			st.geoFailures++
			st.geoRetryAt = time.Now().Add(geoRetryDelay(st.geoFailures))
			st.checkedAt = st.geoRetryAt.Add(-agentDriftCheckEvery)
			return
		}
		st.geoFailures, st.geoRetryAt = 0, time.Time{}
		// A config the agent turned down for lacking these files is worth another try at once.
		st.refused, st.checkedAt = "", time.Time{}
	}()
}

func sendGeoFiles(nodeName string, client *runtime.AgentClient, files []agentGeoFile) error {
	agentGeoSlots <- struct{}{}
	defer func() { <-agentGeoSlots }()
	for _, file := range files {
		logger.Infof("agent %s lacks the geo file %s, sending it", nodeName, file.name)
		stored, err := client.PutGeo(context.Background(), file.name, file.path)
		if err != nil {
			logger.Warningf("send the geo file %s to agent %s: %v", file.name, nodeName, err)
			return err
		}
		logger.Infof("agent %s holds %s now (%d bytes)", nodeName, stored.Name, stored.Size)
	}
	return nil
}
