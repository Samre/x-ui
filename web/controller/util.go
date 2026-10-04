package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"net"
	"net/http"
	"strings"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/web/entity"
)

func getUriId(c *gin.Context) int64 {
	s := struct {
		Id int64 `uri:"id"`
	}{}

	_ = c.BindUri(&s)
	return s.Id
}

func getRemoteIp(c *gin.Context) string {
	value := c.GetHeader("X-Forwarded-For")
	if value != "" {
		ips := strings.Split(value, ",")
		return ips[0]
	} else {
		addr := c.Request.RemoteAddr
		ip, _, _ := net.SplitHostPort(addr)
		return ip
	}
}

func jsonMsg(c *gin.Context, msg string, err error) {
	jsonMsgObj(c, msg, nil, err)
}

func jsonObj(c *gin.Context, obj interface{}, err error) {
	jsonMsgObj(c, "", obj, err)
}

func jsonMsgObj(c *gin.Context, msg string, obj interface{}, err error) {
	m := entity.Msg{
		Obj: obj,
	}
	if err == nil {
		m.Success = true
		if msg != "" {
			m.Msg = msg + "成功"
		}
	} else {
		m.Success = false
		m.Msg = msg + "失败: " + err.Error()
		logger.Warning(msg+"失败: ", err)
	}
	c.JSON(http.StatusOK, m)
}

func pureJsonMsg(c *gin.Context, success bool, msg string) {
	if success {
		c.JSON(http.StatusOK, entity.Msg{
			Success: true,
			Msg:     msg,
		})
	} else {
		c.JSON(http.StatusOK, entity.Msg{
			Success: false,
			Msg:     msg,
		})
	}
}

func html(c *gin.Context, name string, title string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["title"] = title
	data["request_uri"] = c.Request.RequestURI
	data["base_path"] = c.GetString("base_path")
	// 把本次请求的 localizer 放进数据 map，供模板以 {{ i18n . "key" }} 调用。
	// 注意：不能暴露成 {{ .localize "key" }}——Go 模板把那种形式解析成
	// "方法调用 + 参数"，而 map 里存的函数值不被支持，实测必然报错。
	// 因此这里只放 localizer 本身，本地化统一走 web.go 注册的 i18n 函数。
	if localizer, ok := c.Get("localizer"); ok {
		if l, ok := localizer.(*i18n.Localizer); ok && l != nil {
			data["i18n"] = l
		} else {
			logger.Warningf("DIAG localizer 类型不符: %T", localizer)
		}
	} else {
		logger.Warning("DIAG 上下文里没有 localizer")
	}
	c.HTML(http.StatusOK, name, getContext(data))
}

func getContext(h gin.H) gin.H {
	a := gin.H{
		"cur_ver": config.GetVersion(),
	}
	if h != nil {
		for key, value := range h {
			a[key] = value
		}
	}
	return a
}

func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}
