package controller

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"x-ui/web/global"
	"x-ui/web/service"
)

type ServerController struct {
	BaseController

	serverService service.ServerService

	// 下面四个字段同时被 @every 2s 的 cron goroutine 和 HTTP 处理器访问，
	// 必须整体处在 mutex 之下。
	mutex sync.Mutex

	lastStatus        *service.Status
	lastGetStatusTime time.Time

	lastVersions        []string
	lastGetVersionsTime time.Time
}

func NewServerController(g *gin.RouterGroup) *ServerController {
	a := &ServerController{
		lastGetStatusTime: time.Now(),
	}
	a.initRouter(g)
	a.startTask()
	return a
}

func (a *ServerController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/server")

	g.Use(a.checkLogin)
	g.POST("/status", a.status)
	g.POST("/getXrayVersion", a.getXrayVersion)
	g.POST("/installXray/:version", a.installXray)
}

func (a *ServerController) refreshStatus() {
	// 整段持锁：GetStatus 会用上一次的快照算速率增量，中途不能被别的读改写。
	// 发布的是 GetStatus 新建对象的副本，而 HTTP 处理器只读上一次发布的副本，
	// 因此 status handler 可以在锁外安全序列化。
	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.lastStatus = a.serverService.GetStatus(a.lastStatus).Clone()
}

func (a *ServerController) startTask() {
	webServer := global.GetWebServer()
	c := webServer.GetCron()
	c.AddFunc("@every 2s", func() {
		a.mutex.Lock()
		idle := time.Since(a.lastGetStatusTime) > time.Minute*3
		a.mutex.Unlock()
		if idle {
			return
		}
		a.refreshStatus()
	})
}

func (a *ServerController) status(c *gin.Context) {
	a.mutex.Lock()
	a.lastGetStatusTime = time.Now()
	// 交出副本：JSON 序列化发生在锁外，若直接交出指针，
	// cron 下一轮 refreshStatus 改写它就会与序列化并发
	status := a.lastStatus
	a.mutex.Unlock()

	jsonObj(c, status, nil)
}

func (a *ServerController) getXrayVersion(c *gin.Context) {
	a.mutex.Lock()
	cached := a.lastVersions
	fresh := time.Since(a.lastGetVersionsTime) <= time.Minute
	a.mutex.Unlock()
	if fresh {
		jsonObj(c, cached, nil)
		return
	}

	// 网络请求放在锁外，避免一个慢请求把 cron 的 refreshStatus 也堵住
	versions, err := a.serverService.GetXrayVersions()
	if err != nil {
		jsonMsg(c, "获取版本", err)
		return
	}

	a.mutex.Lock()
	a.lastVersions = versions
	a.lastGetVersionsTime = time.Now()
	a.mutex.Unlock()

	jsonObj(c, versions, nil)
}

func (a *ServerController) installXray(c *gin.Context) {
	version := c.Param("version")
	err := a.serverService.UpdateXray(version)
	jsonMsg(c, "安装 xray", err)
}
