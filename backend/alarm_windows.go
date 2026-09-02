//go:build windows

package main

import (
	"log"
	"os/exec"
	"strings"
)

// triggerLocalAlarm plays an attention beep then speaks the issue aloud
// several times via the local machine's speakers, using PowerShell's
// built-in System.Speech synthesizer (no extra install needed on Windows).
// UNTESTED on real Windows hardware - built and cross-compiled from macOS,
// no Windows machine available to verify actual audio output.
func triggerLocalAlarm(cfg *Config, message string) {
	if !cfg.LocalAlarm.Enabled {
		return
	}
	go func() {
		beep := exec.Command("powershell", "-NoProfile", "-Command", "[console]::beep(1000,400)")
		if err := beep.Run(); err != nil {
			log.Printf("local alarm: attention sound failed (continuing to speech): %v", err)
		}

		escaped := strings.ReplaceAll("Attention. "+message, "'", "''")
		speakScript := "Add-Type -AssemblyName System.Speech; " +
			"(New-Object System.Speech.Synthesis.SpeechSynthesizer).Speak('" + escaped + "')"

		for i := 0; i < cfg.LocalAlarm.RepeatCount; i++ {
			cmd := exec.Command("powershell", "-NoProfile", "-Command", speakScript)
			if err := cmd.Run(); err != nil {
				log.Printf("local alarm: speech failed: %v", err)
				return
			}
		}
	}()
}
