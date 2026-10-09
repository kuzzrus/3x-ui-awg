package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// agentGeoSlots bounds the geo files in flight across all agents, so a master that finds a dozen
// agents lacking the same file does not compress and upload a dozen copies at once.
var agentGeoSlots = make(chan struct{}, 4)

// geoExtRef is a routing or DNS entry that reads a database by file name: ext:geoip_runet.dat:ru.
var geoExtRef = regexp.MustCompile(`\bext(?:-ip|-domain)?:([A-Za-z0-9._-]+\.dat)\b`)

// agentGeoFile is a geo file the master can send: its name on the agent and where it lies here.
type agentGeoFile struct{ name, path string }

// agentGeoFilesNeeded lists the geo files a rendered config makes the core read: the default pair for
// geoip:/geosite: rules, the file of every ext: entry, and the files of the geodata section.
func agentGeoFilesNeeded(config []byte) []string {
	text := string(config)
	need := map[string]struct{}{}
	if strings.Contains(text, "geoip:") {
		need["geoip.dat"] = struct{}{}
	}
	if strings.Contains(text, "geosite:") {
		need["geosite.dat"] = struct{}{}
	}
	for _, match := range geoExtRef.FindAllStringSubmatch(text, -1) {
		need[match[1]] = struct{}{}
	}
	var parsed struct {
		Geodata struct {
			Assets []struct {
				File string `json:"file"`
			} `json:"assets"`
		} `json:"geodata"`
	}
	if json.Unmarshal(config, &parsed) == nil {
		for _, asset := range parsed.Geodata.Assets {
			need[asset.File] = struct{}{}
		}
	}
	names := make([]string, 0, len(need))
	for name := range need {
		if agentproto.ValidGeoName(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// geoFilesToSend lists the geo files the config needs that the agent does not hold and this panel
// does. An agent from before the geo endpoint answers 404, and its config is pushed as it always was.
func (s *AgentSyncService) geoFilesToSend(ctx context.Context, client *runtime.AgentClient, config []byte, nodeName string) ([]agentGeoFile, error) {
	needed := agentGeoFilesNeeded(config)
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
			logger.Warningf("agent %s lacks the geo file %s, which this panel has no usable copy of either", nodeName, name)
			continue
		}
		missing = append(missing, agentGeoFile{name: name, path: path})
	}
	return missing, nil
}

// sendGeo sends the files in the background, so the sync tick that waits for every node is not held
// by a slow link. The config goes out on the first tick after they are there.
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
			st.checkedAt = time.Now().Add(agentRetryAfter - agentDriftCheckEvery)
			return
		}
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
