package web

// 临时诊断：看生产注册的 i18n 函数在各种数据形状下返回什么。
// 定位后立即删除。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

func TestDiagLocalize(t *testing.T) {
	s := newTestServer(t)
	engine, err := s.initRouter()
	if err != nil {
		t.Fatalf("initRouter: %v", err)
	}
	fn, ok := engine.FuncMap["i18n"]
	if !ok {
		t.Fatalf("FuncMap 里没有注册 i18n")
	}
	t.Logf("i18n func 类型: %T", fn)

	// 复刻中间件构造 localizer，直接看 Lookup 是否成功
	// 用独立的 bundle 复现：直接读 translation 目录
	b := i18n.NewBundle(language.SimplifiedChinese)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	ents, _ := os.ReadDir("../translation")
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join("../translation", e.Name()))
		if _, err := b.ParseMessageFileBytes(data, e.Name()); err != nil {
			t.Logf("parse %s: %v", e.Name(), err)
		}
	}
	loc := i18n.NewLocalizer(b, "zh-CN")
	for _, key := range []string{"username", "password", "login"} {
		msg, err := loc.Localize(&i18n.LocalizeConfig{MessageID: key})
		if err != nil {
			t.Logf("[zh-CN] %s -> ERROR: %v", key, err)
		} else {
			t.Logf("[zh-CN] %s -> %q", key, msg)
		}
	}

	// 用生产函数校验数据形状
	for _, data := range []interface{}{
		nil,
		map[string]interface{}{"i18n": loc},
		map[string]interface{}{"i18n": nil},
	} {
		res, err := callI18n(t, fn, data, "username")
		t.Logf("data=%T -> res=%q err=%v", data, res, err)
	}

	// 真实渲染片段
	body := performGet(engine, "/", "zh-CN").Body.String()
	if i := indexOf(body, "placeholder"); i >= 0 {
		t.Logf("渲染片段: %q", body[i:minInt(i+45, len(body))])
	}
}

func callI18n(t *testing.T, fn interface{}, data interface{}, key string) (string, error) {
	t.Helper()
	switch f := fn.(type) {
	case func(interface{}, string) (string, error):
		return f(data, key)
	case func(*i18n.Localizer, string) (string, error):
		l, _ := data.(map[string]interface{})["i18n"].(*i18n.Localizer)
		return f(l, key)
	default:
		return "", fmt.Errorf("unexpected func type %T", fn)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
