package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/handlers"
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/scheduler"
	"xui-reseller-bot/internal/services/outbox"
	"xui-reseller-bot/internal/services/reconcile"
	"xui-reseller-bot/internal/services/sync"
	"xui-reseller-bot/internal/xui"
)

func main() {
	if err := config.Load("config.yaml"); err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
	cfg := config.Global

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Connect(ctx, &cfg.Database); err != nil {
		log.Fatalf("Database error: %v", err)
	}
	defer db.Pool.Close()

	if err := db.Migrate(ctx); err != nil {
		log.Fatalf("Migration error: %v", err)
	}

	xuiClient, err := xui.NewClient(&cfg.XUI)
	if err != nil {
		log.Fatalf("XUI Client initialization error: %v", err)
	}

	if status, err := xuiClient.CheckReadiness(ctx); err != nil {
		log.Printf("Warning: 3x-ui readiness check returned error: %v", err)
	} else {
		log.Printf("3x-ui readiness check succeeded: version=%s, inbounds=%d", status.Version, status.InboundsCount)
	}
	xuiClient.StartReadinessChecks(ctx, 30*time.Second)

	reconcileProcessor := reconcile.NewProcessor("resell_bot_reconciler", xuiClient)
	reconcileProcessor.Start(ctx, 30*time.Second)

	syncWorker := sync.NewSyncWorker(xuiClient, 5*time.Minute)
	syncWorker.Start(ctx)

	cache := xui.NewInboundCache(xuiClient, 5*time.Minute)
	xuiClient.Cache = cache
	cache.Start()
	defer cache.Stop()

	scheduler.Start(ctx, cfg)

	bot.Start(&cfg.Bot, xuiClient)

	outboxWorker := outbox.NewWorker(bot.Bot)
	outboxWorker.Start(ctx, 15*time.Second)

	auth := bot.AuthMiddleware()
	admin := bot.AdminMiddleware(&cfg.Admin)

	handlers.RegisterStart(bot.Bot, auth, admin, &cfg.Admin)
	handlers.RegisterTestSub(bot.Bot, auth)
	handlers.RegisterBuySub(bot.Bot, auth)
	handlers.RegisterMyServices(bot.Bot, auth)
	handlers.RegisterWallet(bot.Bot, auth, admin, &cfg.Admin)
	handlers.RegisterAdminMenu(bot.Bot, auth, admin)

	go func() {
		bot.Bot.Start()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	cancel()
	bot.FSM.Close()
	if bot.Bot != nil {
		bot.Bot.Stop()
	}
}
