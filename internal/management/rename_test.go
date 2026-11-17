package management

import (
	"net/http"
	"testing"
)

func TestProvidersCreateRejectsIDsOutsideTheProviderIDGrammar(t *testing.T) {
	env := newEnv(t)
	for _, id := range []string{"a.b", "a/b", "a:b", "a b", ""} {
		rec := env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"`+id+`","wire":"chat","baseURL":"https://gw.example.test/v1","expectedGeneration":0}`)
		assertErrorBody(t, rec, http.StatusBadRequest, "invalid_document")
	}
	rec := env.do(t, http.MethodPost, "/api/v1/providers", `{"id":"gw_2-a","wire":"chat","baseURL":"https://gw.example.test/v1","expectedGeneration":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid id refused: %d %s", rec.Code, rec.Body.String())
	}
}
