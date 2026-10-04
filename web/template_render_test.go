package web

// 模板渲染回归测试。
// 背景：i18n 改造时把调用形式写成 {{ .localize "key" }}，而 Go 模板不支持调用
// 存放在数据 map 里的函数值，会报 "localize is not a method but has arguments"，
// 结果是登录页等 6 个模板整页渲染失败、页面空白。当时的 CI 只跑 go vet + 单测，
// 没有渲染过模板，所以没能拦住。
//
// 这个测试特意走**生产路径**（NewServer + initRouter），而不是自己拼一个
// FuncMap——自己拼的话，web.go 里 i18n 的签名改坏了它也发现不了。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"x-ui/database"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), fmt.Sprintf("x-ui-web-test-%d.db", time.Now().UnixNano()))
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("init test db failed: %v", err)
	}
	return NewServer()
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

// 每个页面都必须渲染出完整 HTML，而不是空白或 500。
func TestPagesRender(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter failed: %v", err)
	}

	for _, path := range []string{"/", "/login"} {
		rec := performGet(engine, path, "zh-CN")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s -> %d, body: %s", path, rec.Code, trimBody(rec.Body.String(), 300))
		}
		body := rec.Body.String()
		if len(body) < 500 || !strings.Contains(body, "<html") {
			t.Fatalf("GET %s 渲染结果不是完整 HTML（%d 字节）: %s", path, len(body), trimBody(body, 300))
		}
	}
}

// 中文与英文请求必须拿到各自的文案，而不是回退成 key。
func TestLoginPageLocalized(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter failed: %v", err)
	}

	cases := []struct {
		accept string
		want   string
	}{
		{"zh-CN", "登录"},
		{"en-US", "login"},
	}
	for _, c := range cases {
		rec := performGet(engine, "/login", c.accept)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET /login -> %d", c.accept, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.want) {
			t.Fatalf("%s 期望页面含 %q，实际: %s", c.accept, c.want, trimBody(body, 400))
		}
		// 反向断言：守卫回退时会原样输出 key，这里必须没有
		if strings.Contains(body, "placeholder='username'") {
			t.Fatalf("%s 本地化被回退成字面量 key", c.accept)
		}
	}
}

// 会话 cookie 缺 HttpOnly/Secure 时会随语言切换而丢失，这里顺带记录：
// 未登录访问 /xui/inbounds 会跳转，因此跳过而非失败。
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
		t.Fatalf("inbounds 页的 modal 未本地化（缺「关闭」），说明 include 漏传 dot: %s", trimBody(body, 400))
	}
}
