package bot

import (
	"log"
	"os"
	"strconv"
	"time"

	"gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/fsm"
	"xui-end-bot/internal/xui"
)

var (
	Bot       *telebot.Bot
	FSM       = fsm.NewFSM()
	GlobalFSM = FSM
	XUIClient *xui.Client
	Locker    = NewKeyLocker()
)

func Start(cfg *config.BotConfig, xuiClient *xui.Client) {
	XUIClient = xuiClient
	FSM = fsm.NewFSM()
	GlobalFSM = FSM

	var poller telebot.Poller

	if cfg.WebhookDomain != "" {
		port := 88
		if cfg.WebhookPort != 0 {
			port = cfg.WebhookPort
		}
		poller = &telebot.Webhook{
			Listen:   ":" + strconv.Itoa(port),
			Endpoint: &telebot.WebhookEndpoint{PublicURL: "https://" + cfg.WebhookDomain + ":" + strconv.Itoa(port)},
			TLS: &telebot.WebhookTLS{
				Key:  cfg.TLSKeyFile,
				Cert: cfg.TLSCertFile,
			},
		}
	} else {
		poller = &telebot.LongPoller{Timeout: 10 * time.Second}
	}

	apiURL := os.Getenv("TELEGRAM_API_URL")
	if apiURL == "" {
		apiURL = "https://api.telegram.org"
	}

	b, err := telebot.NewBot(telebot.Settings{
		URL:    apiURL,
		Token:  cfg.Token,
		Poller: poller,
	})
	if err != nil {
		log.Fatalf("failed to start bot: %v", err)
	}

	Bot = b
	b.Use(middleware.Recover())

	log.Println("Bot starting...")
}

