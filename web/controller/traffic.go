package controller

import (
	"time"

	"github.com/gin-gonic/gin"
	"x-ui/web/service"
)

type TrafficController struct {
	BaseController
}

func NewTrafficController(g *gin.RouterGroup) *TrafficController {
	a := &TrafficController{}
	a.initRouter(g)
	return a
}

func (a *TrafficController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/traffic")
	g.POST("/realtime", a.realtime)
	g.POST("/history", a.history)
}

// realtime 返回最近 30 分钟的秒级网速采样(各节点 + 整机),附带节点名称映射
func (a *TrafficController) realtime(c *gin.Context) {
	jsonObj(c, service.GetTrafficRecorder().GetRecentResult(), nil)
}

// history 返回 [start, end) 区间的历史流量,granularity 为 hour 或 day
func (a *TrafficController) history(c *gin.Context) {
	form := &struct {
		Start       int64  `json:"start"`
		End         int64  `json:"end"`
		Granularity string `json:"granularity"`
	}{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "查询流量历史", err)
		return
	}
	if form.Start <= 0 || form.End <= 0 || form.End <= form.Start {
		now := time.Now().Unix()
		form.Start = now - 7*86400
		form.End = now
	}
	if form.End-form.Start > 31*86400 {
		form.Start = form.End - 31*86400
	}
	if form.Granularity != "day" {
		form.Granularity = "hour"
	}

	settingService := service.SettingService{}
	loc, err := settingService.GetTimeLocation()
	if err != nil {
		loc = time.Local
	}

	result, err := service.GetTrafficRecorder().GetHistory(form.Start, form.End, form.Granularity, loc)
	jsonObj(c, result, err)
}
