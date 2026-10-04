package web

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/web/controller"
	"x-ui/web/job"
	"x-ui/web/network"
	"x-ui/web/service"

	"github.com/BurntSushi/toml"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/language"
)

//go:embed assets/*
var assetsFS embed.FS

//go:embed html/*
var htmlFS embed.FS

//go:embed translation/*
var i18nFS embed.FS

var startTime = time.Now()

type wrapAssetsFS struct {
	embed.FS
}

func (f *wrapAssetsFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open("assets/" + name)
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFile{
		File: file,
	}, nil
}

type wrapAssetsFile struct {
	fs.File
}

func (f *wrapAssetsFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFileInfo{
		FileInfo: info,
	}, nil
}

type wrapAssetsFileInfo struct {
	fs.FileInfo
}

func (f *wrapAssetsFileInfo) ModTime() time.Time {
	return startTime
}

type Server struct {
	httpServer *http.Server
	listener   net.Listener

	index  *controller.IndexController
	server *controller.ServerController
	xui    *controller.XUIController

	xrayService    service.XrayService
	settingService service.SettingService
	inboundService service.InboundService

	cron *cron.Cron

	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Server) getHtmlFiles() ([]string, error) {
	files := make([]string, 0)
	dir, _ := os.Getwd()
	err := fs.WalkDir(os.DirFS(dir), "web/html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Server) getHtmlTemplate(funcMap template.FuncMap) (*template.Template, error) {
	t := template.New("").Funcs(funcMap)
	err := fs.WalkDir(htmlFS, "html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			newT, err := t.ParseFS(htmlFS, path+"/*.html")
			if err != nil {
				// ignore
				return nil
			}
			t = newT
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Server) initRouter() (*gin.Engine, error) {
	if config.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.DefaultWriter = io.Discard
		gin.DefaultErrorWriter = io.Discard
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.Default()

	secret, err := s.settingService.GetSecret()
	if err != nil {
		return nil, err
	}

	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	assetsBasePath := basePath + "assets/"

	store := cookie.NewStore(secret)
	engine.Use(sessions.Sessions("session", store))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", basePath)
	})
	engine.Use(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, assetsBasePath) {
			c.Header("Cache-Control", "max-age=31536000")
		}
	})
	err = s.initI18n(engine)
	if err != nil {
		return nil, err
	}

	if config.IsDebug() {
		// for develop
		files, err := s.getHtmlFiles()
		if err != nil {
			return nil, err
		}
		engine.LoadHTMLFiles(files...)
		engine.StaticFS(basePath+"assets", http.FS(os.DirFS("web/assets")))
	} else {
		// for prod
		t, err := s.getHtmlTemplate(engine.FuncMap)
		if err != nil {
			return nil, err
		}
		engine.SetHTMLTemplate(t)
		engine.StaticFS(basePath+"assets", http.FS(&wrapAssetsFS{FS: assetsFS}))
	}

	g := engine.Group(basePath)

	s.index = controller.NewIndexController(g)
	s.server = controller.NewServerController(g)
	s.xui = controller.NewXUIController(g)

	return engine, nil
}

// DIAG 临时：把诊断写到文件，go test 通过时不会显示 stdout
func diagLog(msg string) {
	fmt.Fprintln(os.Stderr, "DIAG "+msg)
}

func keysOfMap(m map[string]interface{}) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func (s *Server) initI18n(engine *gin.Engine) error {
	bundle := i18n.NewBundle(language.SimplifiedChinese)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	err := fs.WalkDir(i18nFS, "translation", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := i18nFS.ReadFile(path)
		if err != nil {
			return err
		}
		tag, err := bundle.ParseMessageFileBytes(data, path)
		diagLog(fmt.Sprintf("walk path=%s bytes=%d tag=%v err=%v", path, len(data), tag, err))
		return err
	})
	if err != nil {
		return err
	}
	// 验证消息是否真的进了 bundle
	probe := i18n.NewLocalizer(bundle, "zh-CN")
	got, perr := probe.Localize(&i18n.LocalizeConfig{MessageID: "username"})
	diagLog(fmt.Sprintf("bundle 自检 username=%q err=%v", got, perr))

	// 模板里的调用形式必须是 {{ i18n . "key" }}：
	//   - localizer 由 util.go 的 html() 放进每次请求的数据 map，请求局部、不可变，
	//     因此不存在跨请求共享（原实现用包级变量，被中间件写、被渲染读，
	//     同一响应会混进两种语言，实测 63/400 次）；
	//   - 必须走 FuncMap 并把 localizer 当参数传入。实测 {{ .localize "key" }}
	//     在 map/具名 map/struct 各种数据形状下都会报
	//     "localize is not a method but has arguments" —— Go 模板不支持调用
	//     存放在数据里的函数值；
	//   - 代价是拿到 nil dot 的模板无法本地化，所以 include 时必须传 dot。
	engine.FuncMap["i18n"] = func(data interface{}, key string) (string, error) {
		// dot 就是本次请求的数据 map（gin.H），localizer 在其中的 "i18n" 键下。
		// 拿到 nil dot 的模板（例如遗漏传 dot 的 include）无法本地化：
		// 这时回退成 key 本身，而不是让整页渲染失败。
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			diagLog(fmt.Sprintf("dot 类型 %T key=%s", data, key))
			return key, nil
		}
		raw, present := dataMap["i18n"]
		localizer, ok := raw.(*i18n.Localizer)
		if !ok || localizer == nil {
			diagLog(fmt.Sprintf("i18n 缺失 present=%v type=%T key=%s keys=%v", present, raw, key, keysOfMap(dataMap)))
			return key, nil
		}
		diagLog(fmt.Sprintf("命中 key=%s -> %s", key, "ok"))
		return localizer.Localize(&i18n.LocalizeConfig{MessageID: key})
	}

	engine.Use(func(c *gin.Context) {
		localizer := i18n.NewLocalizer(bundle, c.GetHeader("Accept-Language"))
		c.Set("localizer", localizer)
		c.Next()
	})

	return nil
}

func (s *Server) startTask() {
	err := s.xrayService.RestartXray(true)
	if err != nil {
		logger.Warning("start xray failed:", err)
	}
	// 每 30 秒检查一次 xray 是否在运行
	s.cron.AddJob("@every 30s", job.NewCheckXrayRunningJob())

	go func() {
		time.Sleep(time.Second * 5)
		// 每 10 秒统计一次流量，首次启动延迟 5 秒，与重启 xray 的时间错开
		s.cron.AddJob("@every 10s", job.NewXrayTrafficJob())
	}()

	go func() {
		time.Sleep(time.Second * 15)
		// 每 1 分钟把流量增量落库，延迟 15 秒启动以错开 XrayTrafficJob
		s.cron.AddJob("@every 1m", job.NewTrafficPersistJob())
	}()

	// 每 30 秒检查一次 inbound 流量超出和到期的情况
	// （命中时由 CheckInboundJob 立即推送 PushPlus 告警）
	s.cron.AddJob("@every 30s", job.NewCheckInboundJob())
	// PushPlus 通知已改为事件驱动：登录、入站超限/到期时即时推送，
	// 不再按 crontab 定时推送流量汇总，因此设置里没有"通知时间"这一项。
	if enable, err := s.settingService.GetPushPlusEnable(); err == nil && enable {
		logger.Info("PushPlus notify enabled (event-driven)")
	}
}

func (s *Server) Start() (err error) {
	//这是一个匿名函数，没没有函数名
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()

	loc, err := s.settingService.GetTimeLocation()
	if err != nil {
		return err
	}
	s.cron = cron.New(cron.WithLocation(loc), cron.WithSeconds())
	s.cron.Start()

	engine, err := s.initRouter()
	if err != nil {
		return err
	}

	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return err
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return err
	}
	listen, err := s.settingService.GetListen()
	if err != nil {
		return err
	}
	port, err := s.settingService.GetPort()
	if err != nil {
		return err
	}
	listenAddr := net.JoinHostPort(listen, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			listener.Close()
			return err
		}
		c := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		listener = network.NewAutoHttpsListener(listener)
		listener = tls.NewListener(listener, c)
	}

	if certFile != "" || keyFile != "" {
		logger.Info("web server run https on", listener.Addr())
	} else {
		logger.Info("web server run http on", listener.Addr())
	}
	s.listener = listener

	s.startTask()

	s.httpServer = &http.Server{
		Handler: engine,
	}

	go func() {
		s.httpServer.Serve(listener)
	}()

	return nil
}

func (s *Server) Stop() error {
	s.cancel()
	s.xrayService.StopXray()
	if s.cron != nil {
		s.cron.Stop()
	}
	// 停止时把待落库的增量写出。采集任务每 10 秒把增量从 Xray 计数器里取走
	// （GetTraffic 带 reset），落库任务却要等满 1 分钟，因此不在这里补一次
	// Flush 就会让最多 60 秒的每入站流量永久丢失。
	if err := service.GetTrafficPanelService().Flush(); err != nil {
		logger.Warning("flush traffic snapshot on stop failed:", err)
	}
	var err1 error
	var err2 error
	if s.httpServer != nil {
		err1 = s.httpServer.Shutdown(s.ctx)
	}
	if s.listener != nil {
		err2 = s.listener.Close()
	}
	return common.Combine(err1, err2)
}

func (s *Server) GetCtx() context.Context {
	return s.ctx
}

func (s *Server) GetCron() *cron.Cron {
	return s.cron
}
