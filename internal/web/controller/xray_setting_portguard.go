package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/portguard"
)

// portGuardBody is the ports an admin asks to have closed: exactly these, so an empty list opens every one.
type portGuardBody struct {
	Ports []portguard.Port `json:"ports"`
}

// portGuardStatus answers with what is closed, what waits to be confirmed, and the ports that could be closed.
func (a *XraySettingController) portGuardStatus(c *gin.Context) {
	st, err := a.PortGuardService.Status()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	jsonObj(c, st, nil)
}

// portGuardPlan answers with what closing the ports would do, and does none of it.
func (a *XraySettingController) portGuardPlan(c *gin.Context) {
	var b portGuardBody
	if err := c.ShouldBindJSON(&b); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	plan, err := a.PortGuardService.Plan(b.Ports)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, plan, nil)
}

// portGuardApply closes exactly the ports; a change that closes one waits to be confirmed, and the status says until when.
func (a *XraySettingController) portGuardApply(c *gin.Context) {
	var b portGuardBody
	if err := c.ShouldBindJSON(&b); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if _, err := a.PortGuardService.Apply(c.Request.Context(), b.Ports); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	a.portGuardStatus(c)
}

// portGuardConfirm keeps the change that waits to be confirmed.
func (a *XraySettingController) portGuardConfirm(c *gin.Context) {
	if err := a.PortGuardService.Confirm(); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	a.portGuardStatus(c)
}

// portGuardRollback undoes the change that waits to be confirmed.
func (a *XraySettingController) portGuardRollback(c *gin.Context) {
	if err := a.PortGuardService.Rollback(c.Request.Context()); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	a.portGuardStatus(c)
}
