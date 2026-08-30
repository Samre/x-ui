package controller

import (
	"github.com/gin-gonic/gin"
	"x-ui/web/service"
)

type TrafficController struct {
	BaseController

	inboundService service.InboundService
}

func NewTrafficController(g *gin.RouterGroup) *TrafficController {
	a := &TrafficController{}
	a.initRouter(g)
	return a
}

func (a *TrafficController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/traffic")
	g.POST("/realtime", a.realtime)
	g.POST("/overview", a.overview)
}

type TrafficOverviewReq struct {
	Range string `json:"range"`
}

func (a *TrafficController) realtime(c *gin.Context) {
	panel := service.GetTrafficPanelService()
	jsonObj(c, gin.H{
		"samples": panel.GetRealtime(),
	}, nil)
}

func (a *TrafficController) overview(c *gin.Context) {
	req := &TrafficOverviewReq{}
	_ = c.ShouldBindJSON(req)
	result, err := service.GetTrafficPanelService().GetOverview(req.Range, &a.inboundService)
	if err != nil {
		jsonMsg(c, "获取流量统计", err)
		return
	}
	jsonObj(c, result, nil)
}
