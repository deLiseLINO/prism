package management

import (
	"maps"
	"net/http"
	"testing"
)

func TestExperimentalFlagsPersistInTheConfig(t *testing.T) {
	env := newEnv(t)
	want := map[string]bool{"remoteInstall": true}

	assertErrorBody(t, env.do(t, http.MethodPut, "/api/v1/experimental/bad-name", `{"enabled":true}`), http.StatusBadRequest, "invalid_value")

	for _, flag := range []string{"remoteInstall", "otherAgents"} {
		if rec := env.do(t, http.MethodPut, "/api/v1/experimental/"+flag, `{"enabled":true}`); rec.Code != http.StatusOK {
			t.Fatalf("enable %s: status %d, body %s", flag, rec.Code, rec.Body.String())
		}
	}
	off := env.do(t, http.MethodPut, "/api/v1/experimental/otherAgents", `{"enabled":false}`)
	if got := decodeBody[ExperimentalResponse](t, off).Flags; !maps.Equal(got, want) {
		t.Fatalf("after disable = %v, want %v", got, want)
	}

	disk, err := env.cfg.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got := disk.Config.Experimental; !maps.Equal(got, want) {
		t.Fatalf("flags on disk = %v, want %v", got, want)
	}
	if got := decodeBody[ExperimentalResponse](t, env.do(t, http.MethodGet, "/api/v1/experimental", "")).Flags; !maps.Equal(got, want) {
		t.Fatalf("GET flags = %v, want %v", got, want)
	}
}
