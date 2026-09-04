package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
)

// sendNotification sends via the Telegram Bot API directly (plain HTTPS
// POST, no SDK dependency) - no auth scheme complexity, unlike Twilio: the
// bot token is just part of the URL. Returns false (dry-run/logged only) if
// the bot token or chat ID aren't configured yet.
func sendNotification(cfg *Config, message string) (bool, error) {
	token := cfg.Notifications.Telegram.BotToken
	chatID := cfg.Notifications.Telegram.ChatID

	if token == "" || chatID == "" || chatID == "TBD" {
		log.Printf("[DRY RUN - Telegram not configured (bot_token/chat_id unset)] Would send notification: %s", message)
		return false, nil
	}

	return true, telegramSend(token, chatID, message)
}

func telegramSend(token, chatID, text string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	form := url.Values{"chat_id": {chatID}, "text": {text}}

	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var body struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, body.Description)
	}
	return nil
}
