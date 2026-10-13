package controller

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// AgentConnectController is where the agents that dial the panel connect. It is no part of the panel's API and has no
// session: the secret of the agent authenticates it, and any other request gets the bare 404 that an agent answers.
type AgentConnectController struct {
	nodes    service.NodeService
	tunnels  *runtime.AgentTunnels
	upgrader websocket.Upgrader
}

func NewAgentConnectController(g *gin.RouterGroup) *AgentConnectController {
	a := &AgentConnectController{
		tunnels:  runtime.GetAgentTunnels(),
		upgrader: websocket.Upgrader{Subprotocols: []string{agentproto.ConnectProtocol}},
	}
	g.GET("/"+agentproto.ConnectPath+"/:id", a.connect)
	return a
}

func (a *AgentConnectController) connect(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	secret, ok := "", false
	if err == nil {
		secret, ok = a.nodes.PullAgentSecret(id)
	}
	if !ok || !agentproto.CheckBearer(c.GetHeader("Authorization"), secret) ||
		!slices.Contains(websocket.Subprotocols(c.Request), agentproto.ConnectProtocol) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	ws, err := a.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Debugf("agent connect: node %d: %v", id, err)
		return
	}
	if err := a.tunnels.Attach(id, ws); err != nil {
		logger.Warningf("agent connect: node %d: %v", id, err)
		return
	}
	logger.Infof("the agent of node %d connected from %s", id, c.ClientIP())
}
