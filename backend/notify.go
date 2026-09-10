package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// sendNotification sends via the Telegram Bot API directly (plain HTTPS
// POST, no SDK dependency) - no auth scheme complexity, unlike Twilio: the
// bot token is just part of the URL. Returns false (dry-run/logged only) if
// the bot token or chat ID aren't configured yet.
func sendNotification(cfg *Config, message string) (bool, error) {
	return deliver(cfg, message, "")
}

// deliver sends one message to every configured recipient, with the photo at
// imagePath attached when there is one. Delivery is attempted for every
// recipient even after one fails: a bot blocked by one person, or removed
// from one group, must not silently cost everyone else their alert. The
// error returned names whoever failed, and is only non-nil if at least one
// did.
func deliver(cfg *Config, message, imagePath string) (bool, error) {
	token := cfg.Notifications.Telegram.BotToken
	chats := cfg.Notifications.Telegram.recipients()

	if token == "" || len(chats) == 0 {
		log.Printf("[DRY RUN - Telegram not configured (bot_token/chat_id unset)] Would send notification: %s", message)
		return false, nil
	}

	var failures []string
	for _, chatID := range chats {
		var err error
		if imagePath != "" {
			if err = telegramSendPhoto(token, chatID, message, imagePath); err != nil {
				// Fall back to text rather than dropping the alert entirely -
				// a message without its picture still tells someone what
				// happened, which is most of the value.
				log.Printf("sending proof photo to %s failed (%v) - falling back to text only", chatID, err)
				err = telegramSend(token, chatID, message)
			}
		} else {
			err = telegramSend(token, chatID, message)
		}
		if err != nil {
			log.Printf("notifying %s failed: %v", chatID, err)
			failures = append(failures, chatID)
		}
	}

	if len(failures) > 0 {
		return true, fmt.Errorf("failed to notify %d of %d recipients (%s)",
			len(failures), len(chats), strings.Join(failures, ", "))
	}
	return true, nil
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

// sendNotificationPhoto sends an image with the message as its caption, used
// for the compliant alert so the recipient sees the evidence rather than
// having to take the detector's word for it. Falls back to a plain text send
// if the image is missing or unreadable - losing the whole notification
// because the proof frame vanished would be a far worse failure than sending
// it without a picture.
func sendNotificationPhoto(cfg *Config, message, imagePath string) (bool, error) {
	return deliver(cfg, message, imagePath)
}

func telegramSendPhoto(token, chatID, caption, imagePath string) error {
	file, err := os.Open(imagePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", chatID); err != nil {
		return err
	}
	// Telegram caps photo captions at 1024 characters; both messages are far
	// short of that, so this is a guard rather than a real constraint.
	if err := w.WriteField("caption", caption); err != nil {
		return err
	}
	part, err := w.CreateFormFile("photo", filepath.Base(imagePath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", token)
	resp, err := http.Post(endpoint, w.FormDataContentType(), &buf)
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
