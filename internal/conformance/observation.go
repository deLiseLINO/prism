package conformance

import (
	"encoding/json"
	"strings"
)

type Header struct{ Name, Value string }

type Headers []Header

func (hs Headers) Get(name string) (string, bool) {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

type Record struct {
	Status   int
	Headers  Headers
	JSON     any
	RawBytes int
}

type NormalizedEvent struct {
	Event   string
	Data    any
	Ordinal int
}

type ToolCallProjection struct {
	ID        string
	Name      string
	Arguments any
	Kind      string
	Ordinal   int
}

type McpCallProjection struct{ Namespace, Name string }

type Observation struct {
	Client    ClientObservation
	Upstream  UpstreamObservation
	Process   ProcessObservation
	Verifiers map[string]any
}

type ClientObservation struct {
	Request  Record
	Response ResponseRecord
}

type ResponseRecord struct {
	Status         int
	Headers        Headers
	JSON           any
	Events         []NormalizedEvent
	ToolCalls      []ToolCallProjection
	McpCalls       []McpCallProjection
	Terminal       *string
	NormalizedText string
}

type UpstreamObservation struct {
	Requests  []Record
	Responses []any
}

type ProcessObservation struct{ ExitCode *int }

func Empty() *Observation {
	return &Observation{
		Client: ClientObservation{
			Response: ResponseRecord{
				Events:    []NormalizedEvent{},
				ToolCalls: []ToolCallProjection{},
				McpCalls:  []McpCallProjection{},
			},
		},
		Upstream:  UpstreamObservation{Requests: []Record{}, Responses: []any{}},
		Verifiers: map[string]any{},
	}
}

func RecordUpstreamRequest(o *Observation, req *UpstreamRequest) {
	var parsed any
	_ = json.Unmarshal(req.Body, &parsed)
	o.Upstream.Requests = append(o.Upstream.Requests, Record{Headers: req.Headers, JSON: parsed, RawBytes: len(req.Body)})
}

func (o *Observation) JSON() ([]byte, error) {
	obs := map[string]any{
		"client": map[string]any{
			"request": recordJSON(o.Client.Request),
			"response": map[string]any{
				"status":         o.Client.Response.Status,
				"headers":        headersJSON(o.Client.Response.Headers),
				"json":           o.Client.Response.JSON,
				"events":         eventsJSON(o.Client.Response.Events),
				"toolCalls":      toolCallsJSON(o.Client.Response.ToolCalls),
				"mcpCalls":       mcpCallsJSON(o.Client.Response.McpCalls),
				"terminal":       o.Client.Response.Terminal,
				"normalizedText": o.Client.Response.NormalizedText,
			},
		},
		"upstream": map[string]any{
			"requests":  upstreamRequestsJSON(o.Upstream.Requests),
			"responses": o.Upstream.Responses,
		},
		"process":   map[string]any{"exitCode": o.Process.ExitCode},
		"verifiers": o.Verifiers,
	}
	return json.Marshal(obs)
}

func recordJSON(r Record) map[string]any {
	return map[string]any{
		"status": r.Status, "headers": headersJSON(r.Headers), "json": r.JSON, "rawBytes": r.RawBytes,
	}
}

func headersJSON(hs Headers) map[string]string {
	out := map[string]string{}
	for _, h := range hs {
		out[strings.ToLower(h.Name)] = h.Value
	}
	return out
}

func upstreamRequestsJSON(reqs []Record) []any {
	out := make([]any, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, recordJSON(r))
	}
	return out
}

func eventsJSON(events []NormalizedEvent) []any {
	out := make([]any, 0, len(events))
	for _, ev := range events {
		out = append(out, map[string]any{
			"event": ev.Event, "data": ev.Data, "ordinal": ev.Ordinal,
		})
	}
	return out
}

func toolCallsJSON(calls []ToolCallProjection) []any {
	out := make([]any, 0, len(calls))
	for _, c := range calls {
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "arguments": c.Arguments, "kind": c.Kind, "ordinal": c.Ordinal,
		})
	}
	return out
}

func mcpCallsJSON(calls []McpCallProjection) []any {
	out := make([]any, 0, len(calls))
	for _, c := range calls {
		out = append(out, map[string]any{
			"namespace": c.Namespace, "name": c.Name,
		})
	}
	return out
}
