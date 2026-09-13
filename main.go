// Command xui-remote is a Telegram bot for remotely configuring a 3x-ui panel.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lichenhao/xui-remote/internal/bot"
	"github.com/lichenhao/xui-remote/internal/config"
	"github.com/lichenhao/xui-remote/internal/modules/client"
	"github.com/lichenhao/xui-remote/internal/modules/configtxn"
	"github.com/lichenhao/xui-remote/internal/modules/inbound"
	"github.com/lichenhao/xui-remote/internal/modules/outbound"
	"github.com/lichenhao/xui-remote/internal/modules/routetest"
	"github.com/lichenhao/xui-remote/internal/modules/routing"
	"github.com/lichenhao/xui-remote/internal/modules/status"
	"github.com/lichenhao/xui-remote/internal/panel"
	"github.com/lichenhao/xui-remote/internal/state"
	"github.com/lichenhao/xui-remote/internal/txn"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	checkOnly := flag.Bool("check", false, "verify panel connectivity and exit")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	api := panel.New(panel.Options{
		BaseURL:     cfg.Panel.BaseURL,
		Token:       cfg.Panel.APIToken,
		PublicHost:  cfg.Panel.PublicHost,
		Timeout:     time.Duration(cfg.Panel.TimeoutSeconds) * time.Second,
		InsecureTLS: cfg.Panel.InsecureTLS,
	})

	st, err := api.Status()
	if err != nil {
		log.Fatalf("连接面板失败: %v", err)
	}
	log.Printf("已连接面板 v%s，Xray %s %s", st.PanelVersion, st.Xray.Version, st.Xray.State)
	if *checkOnly {
		return
	}

	store, err := state.NewStore(cfg.State.Dir, cfg.State.MaxSnapshots)
	if err != nil {
		log.Fatalf("状态目录不可用: %v", err)
	}
	manager := txn.New(api, store)

	b, err := bot.New(cfg.Telegram.Token, cfg.Telegram.AllowedChatIDs)
	if err != nil {
		log.Fatalf("创建 bot 失败: %v", err)
	}
	b.Register(
		routing.New(manager),
		outbound.New(api, manager),
		inbound.New(api),
		routetest.New(api),
		configtxn.New(manager),
		client.New(api),
		status.New(api),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("bot 已启动，白名单 chat: %v", cfg.Telegram.AllowedChatIDs)
	if err := b.Start(ctx); err != nil {
		log.Fatalf("bot 退出: %v", err)
	}
}
