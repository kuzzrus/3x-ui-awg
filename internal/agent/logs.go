package agent

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const defaultLogLines = 100

// getLogs serves the newest lines of the agent's log buffer, which also holds what the core printed.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	count := defaultLogLines
	if value := query.Get(agentproto.QueryLogCount); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a positive number", agentproto.QueryLogCount))
			return
		}
		count = min(parsed, agentproto.MaxLogLines)
	}
	level := strings.ToLower(query.Get(agentproto.QueryLogLevel))
	switch level {
	case "":
		level = "info"
	case "err":
		level = "error"
	}
	if _, err := logging.LogLevel(level); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be debug, info, notice, warning or error", agentproto.QueryLogLevel))
		return
	}
	lines := logger.GetLogs(count, level)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, agentproto.Logs{Lines: lines})
}
