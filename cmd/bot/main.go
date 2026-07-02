package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/bot/handlers"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/scheduler"
	"xui-end-bot/internal/xui"
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

	cache := xui.NewInboundCache(xuiClient, 5*time.Minute)
	xuiClient.Cache = cache
	cache.Start()
	defer cache.Stop()

	scheduler.Start(ctx, cfg)

	bot.Start(&cfg.Bot, xuiClient)

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

