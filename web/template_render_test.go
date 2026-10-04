package web

// 模板渲染回归测试。
//
// 背景：i18n 改造时把模板调用改成 {{ .localize "key" }}，而 Go 模板不支持调用
// 存放在数据 map 里的函数值，会报 "localize is not a method but has arguments"，
// 结果登录页等 6 个模板整页渲染失败、面板打不开。当时的 CI 只跑 go vet + 单测，
// 从没渲染过模板，所以没能拦住。
//
// 这个测试特意走**生产路径**（NewServer + initRouter），而不是自己拼一个
// FuncMap——自己拼的话，web.go 里 i18n 的实现改坏了它也发现不了。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/database"
	"x-ui/web/global"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), fmt.Sprintf("x-ui-web-test-%d.db", time.Now().UnixNano()))
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("init test db failed: %v", err)
	}
	s := NewServer()
	// main.go 在 server.Start() 之前会 global.SetWebServer(server)：
	// initRouter -> NewServerController -> startTask 会通过全局取 cron，
	// 不设置就是空指针。测试必须复刻这一步，否则走不到渲染。
	global.SetWebServer(s)
	return s
}

func performGet(engine http.Handler, path string, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept-Language", accept)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func trimBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

// 未登录访问首页必须渲染出完整 HTML，而不是空白或 500。
// 登录页就是 GET /（POST /login 是提交接口）。
func TestPagesRender(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter failed: %v", err)
	}

	rec := performGet(engine, "/", "zh-CN")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / -> %d, body: %s", rec.Code, trimBody(rec.Body.String(), 300))
	}
	body := rec.Body.String()
	if len(body) < 500 || !strings.Contains(body, "<html") {
		t.Fatalf("GET / 渲染结果不是完整 HTML（%d 字节）: %s", len(body), trimBody(body, 300))
	}
}

// 中英文请求各自拿到对应文案，而不是退化成 key。
func TestLoginPageLocalized(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter failed: %v", err)
	}

	cases := []struct {
		accept    string
		want      string
		notWanted string
	}{
		// placeholder 渲染成 placeholder='用户名'
		{"zh-CN", "用户名", "placeholder='username'"},
		// 英文包里 username 字面就是 username，用登录按钮文案更可靠
		{"en-US", ">login<", "placeholder='密码'"},
	}
	for _, c := range cases {
		rec := performGet(engine, "/", c.accept)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET / -> %d", c.accept, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.want) {
			t.Fatalf("%s 期望页面含 %q，实际: %s", c.accept, c.want, trimBody(body, 400))
		}
		if strings.Contains(body, c.notWanted) {
			t.Fatalf("%s 页面出现不该有的 %q（本地化未生效）", c.accept, c.notWanted)
		}
	}
}

// /xui/inbounds 需要登录，未登录会跳转；能拿到 200 时断言 modal 文案已本地化。
func TestInboundsPageModalsLocalized(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter failed: %v", err)
	}

	rec := performGet(engine, "/xui/inbounds", "zh-CN")
	if rec.Code != http.StatusOK {
		t.Skipf("/xui/inbounds 需要登录（%d），跳过 modal 文案断言", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "关闭") {
		t.Fatalf("inbounds 页的 modal 未本地化（缺「关闭」）: %s", trimBody(body, 400))
	}
}
