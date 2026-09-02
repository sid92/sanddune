package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// sendNotification sends via Twilio's REST API directly (Basic Auth + form
// POST) - no SDK dependency needed. Returns false (dry-run/logged only) if
// credentials or the destination number aren't configured yet.
func sendNotification(cfg *Config, message string) (bool, error) {
	sid := cfg.Notifications.Twilio.AccountSID
	token := cfg.Notifications.Twilio.AuthToken
	from := cfg.Notifications.Twilio.FromNumber
	to := cfg.Notifications.PhoneNumber
	channel := cfg.Notifications.Twilio.Channel

	if sid == "" || token == "" || from == "" || to == "" || to == "TBD" {
		log.Printf("[DRY RUN - Twilio not configured or phone_number is still unset] Would send %s notification: %s", channel, message)
		return false, nil
	}

	if channel == "whatsapp" {
		from = "whatsapp:" + from
		to = "whatsapp:" + to
	}

	return true, twilioSend(sid, token, from, to, message)
}

func twilioSend(sid, token, from, to, body string) error {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", sid)
	form := url.Values{"To": {to}, "From": {from}, "Body": {body}}

	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(sid, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("twilio API returned status %d", resp.StatusCode)
	}
	return nil
}
