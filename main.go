// Command xui-remote is a Telegram bot for remotely configuring a 3x-ui panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sleepingF0x/xui-remote/internal/bot"
	"github.com/sleepingF0x/xui-remote/internal/config"
	"github.com/sleepingF0x/xui-remote/internal/modules/client"
	"github.com/sleepingF0x/xui-remote/internal/modules/configtxn"
	"github.com/sleepingF0x/xui-remote/internal/modules/inbound"
	"github.com/sleepingF0x/xui-remote/internal/modules/outbound"
	"github.com/sleepingF0x/xui-remote/internal/modules/routetest"
	"github.com/sleepingF0x/xui-remote/internal/modules/routing"
	"github.com/sleepingF0x/xui-remote/internal/modules/status"
	"github.com/sleepingF0x/xui-remote/internal/panel"
	"github.com/sleepingF0x/xui-remote/internal/selftest"
	"github.com/sleepingF0x/xui-remote/internal/state"
	"github.com/sleepingF0x/xui-remote/internal/txn"
	"github.com/sleepingF0x/xui-remote/internal/verify"
)

// version is set at release build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	checkOnly := flag.Bool("check", false, "verify panel connectivity and exit")
	selfTest := flag.Bool("selftest", false, "exercise every read path against the panel and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("xui-remote", version)
		return
	}
	log.Printf("xui-remote %s", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	for _, w := range cfg.Warnings() {
		log.Printf("配置警告: %s", w)
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
	if *selfTest {
		if err := selftest.New(api, os.Stdout).Run(); err != nil {
			log.Fatal(err)
		}
		return
	}

	store, err := state.NewStore(cfg.State.Dir, cfg.State.MaxSnapshots)
	if err != nil {
		log.Fatalf("状态目录不可用: %v", err)
	}
	manager := txn.New(api, store,
		txn.WithConfirmWindow(time.Duration(*cfg.Apply.ConfirmMinutes)*time.Minute))

	b, err := bot.New(cfg.Telegram.Token, cfg.Telegram.AllowedChatIDs)
	if err != nil {
		log.Fatalf("创建 bot 失败: %v", err)
	}
	b.Register(
		routing.New(manager),
		outbound.New(api, manager),
		inbound.New(api),
		routetest.New(api, manager),
		configtxn.New(manager, verify.New(api)),
		client.New(api, store),
		status.New(api),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("bot 已启动，白名单 chat: %v", cfg.Telegram.AllowedChatIDs)
	if err := b.Start(ctx); err != nil {
		log.Fatalf("bot 退出: %v", err)
	}
}
