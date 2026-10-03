package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const registrationFile = "daemon.json"

type Registration struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	URL     string `json:"url"`
	PID     int    `json:"pid"`
}

func StateDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".prism")
	}
	return ".prism"
}

func registrationPath(stateDir string) string {
	return filepath.Join(stateDir, registrationFile)
}

func ReadRegistration(stateDir string) (Registration, error) {
	data, err := os.ReadFile(registrationPath(stateDir))
	if err != nil {
		return Registration{}, err
	}
	var reg Registration
	if err := json.Unmarshal(data, &reg); err != nil {
		return Registration{}, err
	}
	return reg, nil
}

func WriteRegistration(stateDir string, reg Registration) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, registrationFile+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), registrationPath(stateDir))
}

func removeRegistration(stateDir string) error {
	if err := os.Remove(registrationPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func RemoveOwnRegistration(stateDir, id string) error {
	reg, err := ReadRegistration(stateDir)
	if err != nil || reg.ID != id {
		return nil
	}
	return removeRegistration(stateDir)
}
