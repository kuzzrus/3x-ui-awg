package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// decoySiteUploadLimit bounds the body of an upload before any of it is kept: the zip, and the framing around it.
const decoySiteUploadLimit = agentproto.MaxDecoySiteBytes + 1<<20

// decoySites lists the static sites that agent nodes can serve as their decoy.
func (a *NodeController) decoySites(c *gin.Context) {
	sites, err := a.decoySiteService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.decoySites"), err)
		return
	}
	jsonObj(c, sites, nil)
}

// uploadDecoySite keeps the zip of a static site, sent as multipart like the upload of the panel's own decoy.
func (a *NodeController) uploadDecoySite(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, decoySiteUploadLimit)
	file, header, err := c.Request.FormFile("site")
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.uploadDecoySite"), err)
		return
	}
	defer file.Close()
	site, err := a.decoySiteService.Add(header.Filename, file, header.Size)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.uploadDecoySite"), err)
		return
	}
	jsonObj(c, site, nil)
}

// delDecoySite removes a site that no node serves.
func (a *NodeController) delDecoySite(c *gin.Context) {
	if err := a.decoySiteService.Delete(c.Param("sha")); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.deleteDecoySite"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.nodes.toasts.deleteDecoySite"), nil)
}
