package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ActiveIntervalMinSec     = 15
	ActiveIntervalMaxSec     = 600
	BackgroundIntervalMinSec = 60
	BackgroundIntervalMaxSec = 3600
)

type Settings struct {
	AutoRefreshEnabled    bool `json:"auto_refresh_enabled"`
	ActiveIntervalSec     int  `json:"active_interval_sec"`
	BackgroundIntervalSec int  `json:"background_interval_sec"`
}

type UIState struct {
	CompactMode      bool   `json:"compact_mode"`
	ActiveAccountKey string `json:"active_account_key"`
}

func DefaultSettings() Settings {
	return Settings{
		AutoRefreshEnabled:    true,
		ActiveIntervalSec:     30,
		BackgroundIntervalSec: 300,
	}
}

func LoadSettings() (Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return DefaultSettings(), err
	}
	root, err := readJSONMap(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSettings(), nil
		}
		return DefaultSettings(), fmt.Errorf("failed to read settings: %w", err)
	}

	settings := DefaultSettings()
	if enabled, ok := root["auto_refresh_enabled"].(bool); ok {
		settings.AutoRefreshEnabled = enabled
	}
	if raw, ok := asInt(root["active_interval_sec"]); ok {
		settings.ActiveIntervalSec = ClampInt(int(raw), ActiveIntervalMinSec, ActiveIntervalMaxSec)
	}
	if raw, ok := asInt(root["background_interval_sec"]); ok {
		settings.BackgroundIntervalSec = ClampInt(int(raw), BackgroundIntervalMinSec, BackgroundIntervalMaxSec)
	}
	return settings, nil
}

func SaveSettings(settings Settings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	root := map[string]any{
		"auto_refresh_enabled":    settings.AutoRefreshEnabled,
		"active_interval_sec":     settings.ActiveIntervalSec,
		"background_interval_sec": settings.BackgroundIntervalSec,
	}
	return writeJSONMap(path, root)
}

func LoadUIState() (UIState, error) {
	path, err := uiStatePath()
	if err != nil {
		return UIState{}, err
	}
	root, err := readJSONMap(path)
	if err != nil {
		if os.IsNotExist(err) {
			return UIState{}, nil
		}
		return UIState{}, fmt.Errorf("failed to read ui state: %w", err)
	}

	state := UIState{}
	if compact, ok := root["compact_mode"].(bool); ok {
		state.CompactMode = compact
	}
	if active, ok := root["active_account_key"].(string); ok {
		state.ActiveAccountKey = strings.TrimSpace(active)
	}
	return state, nil
}

func SaveUIState(state UIState) error {
	path, err := uiStatePath()
	if err != nil {
		return err
	}
	root := map[string]any{
		"compact_mode":       state.CompactMode,
		"active_account_key": strings.TrimSpace(state.ActiveAccountKey),
	}
	return writeJSONMap(path, root)
}

func ClampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func settingsPath() (string, error) {
	dir, err := prismUIDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

func uiStatePath() (string, error) {
	dir, err := prismUIDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ui_state.json"), nil
}
