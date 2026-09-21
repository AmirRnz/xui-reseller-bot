package bot

import (
	"context"
	"log"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

func AuthMiddleware() telebot.MiddlewareFunc {
	return func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			if c.Sender() == nil {
				return next(c)
			}

			telegramID := c.Sender().ID
			user, err := db.GetUserByTelegramID(context.Background(), telegramID)

			if err != nil {
				log.Printf("AuthMiddleware: DB error: %v", err)
				return c.Send("خطای داخلی رخ داد. لطفا مجددا تلاش کنید.")
			}

			if user == nil {
				user = &db.User{
					TelegramID: telegramID,
					Username:   c.Sender().Username,
					FirstName:  c.Sender().FirstName,
					LastName:   c.Sender().LastName,
					Language:   "fa",
					Status:     db.UserStatusPending,
				}
				if err := db.CreateUser(context.Background(), user); err != nil {
					log.Printf("AuthMiddleware: Failed to create user: %v", err)
					return c.Send("خطایی در ثبت نام شما رخ داد.")
				}
			}

			if user.Status == "banned" {
				return c.Send("حساب کاربری شما مسدود شده است.")
			}

			c.Set("user", user)
			return next(c)
		}
	}
}

func AdminMiddleware(adminCfg *config.AdminConfig) telebot.MiddlewareFunc {
	return func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			if c.Sender() == nil {
				return next(c)
			}

			isAdmin := false
			for _, id := range adminCfg.AdminIDs {
				if c.Sender().ID == id {
					isAdmin = true
					break
				}
			}

			if !isAdmin {
				return c.Send("شما دسترسی لازم برای استفاده از این بخش را ندارید.")
			}

			return next(c)
		}
	}
}
