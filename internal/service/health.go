package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/management"
)

const (
	healthPath    = "/api/v1/health"
	healthTimeout = 2 * time.Second
)

func Probe(ctx context.Context, reg Registration) bool {
	if reg.ID == "" || reg.URL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(reg.URL, "/")+healthPath, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body management.HealthResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body) != nil {
		return false
	}
	return body.Status == "ok" && body.ID == reg.ID
}
