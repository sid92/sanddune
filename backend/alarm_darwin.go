//go:build darwin

package main

import (
	"log"
	"os/exec"
)

// triggerLocalAlarm plays an attention sound then speaks the issue aloud
// several times via the local machine's speakers. macOS-specific (`afplay`
// and `say`, both built in) - a Windows/Linux port would need a different
// mechanism (e.g. SAPI or espeak) here.
func triggerLocalAlarm(cfg *Config, message string) {
	if !cfg.LocalAlarm.Enabled {
		return
	}
	go func() {
		if err := exec.Command("afplay", "/System/Library/Sounds/Sosumi.aiff").Run(); err != nil {
			log.Printf("local alarm: attention sound failed (continuing to speech): %v", err)
		}
		for i := 0; i < cfg.LocalAlarm.RepeatCount; i++ {
			if err := exec.Command("say", "Attention. "+message).Run(); err != nil {
				log.Printf("local alarm: speech failed: %v", err)
				return
			}
		}
	}()
}
