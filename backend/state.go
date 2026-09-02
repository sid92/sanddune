package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func loadState(name string) map[string]any {
	path := filepath.Join(stateDir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

func saveState(name string, data map[string]any) error {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(stateDir, name+".json")
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
