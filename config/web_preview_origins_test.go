package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// web.preview_origins：Vercel 预览允许的 origin 白名单，config.json 持久配置；
// XBOT_PREVIEW_ORIGINS 一旦设置（哪怕为空）就整体覆盖它 —— 空值 = 显式关闭。
func TestWebPreviewOriginsConfigAndEnvOverride(t *testing.T) {
	var cfg Config
	raw := `{"web":{"preview_origins":["https://xbot-preview-*-team.vercel.app","http://localhost:5173"]}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	want := []string{"https://xbot-preview-*-team.vercel.app", "http://localhost:5173"}
	if !reflect.DeepEqual(cfg.Web.PreviewOrigins, want) {
		t.Fatalf("config.json preview_origins = %#v, want %#v", cfg.Web.PreviewOrigins, want)
	}

	t.Run("unset env keeps config", func(t *testing.T) {
		t.Setenv("XBOT_PREVIEW_ORIGINS", "") // restores the caller's value afterwards
		os.Unsetenv("XBOT_PREVIEW_ORIGINS")
		c := cfg
		applyEnvOverrides(&c)
		if !reflect.DeepEqual(c.Web.PreviewOrigins, want) {
			t.Fatalf("got %#v", c.Web.PreviewOrigins)
		}
	})
	t.Run("env replaces config", func(t *testing.T) {
		t.Setenv("XBOT_PREVIEW_ORIGINS", " https://a.example , ,https://b.example ")
		c := cfg
		applyEnvOverrides(&c)
		if got := c.Web.PreviewOrigins; !reflect.DeepEqual(got, []string{"https://a.example", "https://b.example"}) {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("empty env disables", func(t *testing.T) {
		t.Setenv("XBOT_PREVIEW_ORIGINS", "")
		c := cfg
		applyEnvOverrides(&c)
		if len(c.Web.PreviewOrigins) != 0 {
			t.Fatalf("empty env must disable preview origins, got %#v", c.Web.PreviewOrigins)
		}
	})
}
