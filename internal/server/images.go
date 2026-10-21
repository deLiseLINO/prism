package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/routing"
	"github.com/deLiseLINO/prism/internal/usage"
)

const imagesProtocol = "images"

type imageRequestError struct {
	status  int
	code    string
	param   string
	message string
}

func (e *imageRequestError) Error() string { return e.message }

// handleImageGenerations serves POST /v1/images/generations: the body is
// validated against the supported subset, model is rewritten to the
// provider's own id, and the upstream answer is relayed as received.
func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	model, body, err := parseImageRequest(r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeImageError(w, http.StatusRequestEntityTooLarge, "request_too_large", "", "request body exceeds "+strconv.FormatInt(mbe.Limit, 10)+" bytes")
			return
		}
		var re *imageRequestError
		if errors.As(err, &re) {
			writeImageError(w, re.status, re.code, re.param, re.message)
			return
		}
		writeImageError(w, http.StatusBadRequest, "invalid_request", "", err.Error())
		return
	}
	providerID, name, _ := strings.Cut(model, "/")
	target, err := imageTargetFor(s.cfg.Get().Config, providerID, name)
	if err != nil {
		writeImageError(w, http.StatusNotFound, "model_not_found", "model", "model "+strconv.Quote(model)+" is not an image generation model")
		return
	}
	rewritten, err := json.Marshal(string(target.Model))
	if err != nil {
		writeImageError(w, http.StatusInternalServerError, "internal", "", err.Error())
		return
	}
	body["model"] = rewritten
	upstream, err := json.Marshal(body)
	if err != nil {
		writeImageError(w, http.StatusInternalServerError, "internal", "", err.Error())
		return
	}
	facts := execution.Facts{RequestID: execution.RequestID(newRequestID()), Client: execution.ClientOMP}
	start := s.clock.Now()
	res := s.images.Generate(r.Context(), canon.ModelID(model), target, facts, upstream)
	s.recordImageUsage(facts, model, target, res, start)
	if res.Failure != nil {
		if res.Failure.Reason == canon.FailClientClosed {
			return
		}
		status := failureStatus(*res.Failure)
		writeImageError(w, status, "upstream_error", "", res.Failure.Message)
		return
	}
	relayImageResponse(w, res.Response)
}

func parseImageRequest(r *http.Request) (string, map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return "", nil, invalidImageField("", "request body must be a JSON object")
	}
	var model string
	if json.Unmarshal(body["model"], &model) != nil || model == "" {
		return "", nil, invalidImageField("model", "model is required")
	}
	var prompt string
	if json.Unmarshal(body["prompt"], &prompt) != nil || strings.TrimSpace(prompt) == "" {
		return "", nil, invalidImageField("prompt", "prompt is required")
	}
	if raw, ok := present(body, "n"); ok {
		var n float64
		if json.Unmarshal(raw, &n) != nil || n != 1 {
			return "", nil, invalidImageField("n", "only n=1 is supported")
		}
	}
	if raw, ok := present(body, "response_format"); ok {
		var format string
		if json.Unmarshal(raw, &format) != nil || format != "b64_json" {
			return "", nil, invalidImageField("response_format", `only response_format "b64_json" is supported`)
		}
	}
	if raw, ok := present(body, "stream"); ok {
		var stream bool
		if json.Unmarshal(raw, &stream) != nil || stream {
			return "", nil, invalidImageField("stream", "streaming image responses are not supported")
		}
	}
	return model, body, nil
}

func present(body map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	raw, ok := body[key]
	if !ok || string(raw) == "null" {
		return nil, false
	}
	return raw, true
}

func invalidImageField(param, message string) error {
	return &imageRequestError{status: http.StatusBadRequest, code: "invalid_request", param: param, message: message}
}

// relayImageResponse passes the upstream answer through. A JSON body is
// relayed verbatim with its status; anything else becomes an error in the
// OpenAI shape so clients can always parse the response.
func relayImageResponse(w http.ResponseWriter, resp provider.ImageResponse) {
	if json.Valid(resp.Body) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(resp.Body)))
		w.WriteHeader(resp.Status)
		if _, err := w.Write(resp.Body); err != nil {
			log.Printf("server: write image response: %v", err)
		}
		return
	}
	status := resp.Status
	message := strings.TrimSpace(string(resp.Body))
	if status >= 200 && status < 300 {
		status = http.StatusBadGateway
		message = "upstream returned a non-JSON image response"
	}
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = "upstream status " + strconv.Itoa(resp.Status)
	}
	writeImageError(w, status, "upstream_error", "", message)
}

func writeImageError(w http.ResponseWriter, status int, code, param, message string) {
	writeJSON(w, status, chatErrorEnvelope{Error: chatErrorBody{
		Message: message,
		Type:    imageErrorType(status),
		Code:    code,
		Param:   param,
	}})
}

func imageErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

func (s *Server) recordImageUsage(facts execution.Facts, model string, target provider.Target, res routing.ImageResult, start time.Time) {
	if s.usage == nil {
		return
	}
	rec := usage.Record{
		RequestID: string(facts.RequestID),
		Timestamp: start,
		Model:     model,
		Protocol:  imagesProtocol,
		Status:    res.Outcome,
		UsageKind: usage.StatusZero,
		Duration:  s.clock.Now().Sub(start),
	}
	if res.Attempted {
		rec.Attempts = []usage.Attempt{{Provider: string(target.Provider), Model: string(target.Model), Outcome: res.Outcome}}
	}
	if res.Outcome != "completed" {
		rec.Reason = usage.FailureName(res.Reason)
	}
	if err := s.usage.Insert(context.Background(), rec); err != nil {
		log.Printf("server: usage record: %v", err)
	}
}
