package management

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/deLiseLINO/prism/internal/config"
)

type ExperimentalResponse struct {
	Flags map[string]bool `json:"flags"`
}

type ExperimentalFlagWrite struct {
	Enabled bool `json:"enabled"`
}

var experimentalFlagName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)

func (s *Server) experimentalGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ExperimentalResponse{Flags: onFlags(s.cfg.Get().Config.Experimental)})
}

// experimentalFlagPut sets one flag. It writes over the current generation
// instead of a caller's one: the write touches only this flag, so a config
// change made in between cannot be lost.
func (s *Server) experimentalFlagPut(w http.ResponseWriter, r *http.Request) {
	flag := r.PathValue("flag")
	if !experimentalFlagName.MatchString(flag) {
		writeError(w, http.StatusBadRequest, "invalid_value", "experimental flag name must be letters and digits")
		return
	}
	body, ok := decodeJSON[ExperimentalFlagWrite](w, r)
	if !ok {
		return
	}
	for attempt := 1; ; attempt++ {
		snap := s.cfg.Get()
		doc := snap.Config
		if doc.Experimental == nil {
			doc.Experimental = map[string]bool{}
		}
		if body.Enabled {
			doc.Experimental[flag] = true
		} else {
			delete(doc.Experimental, flag)
		}
		updated, err := s.cfg.Update(doc, snap.Generation)
		if errors.Is(err, config.ErrStaleGeneration) && attempt < 3 {
			continue
		}
		if err != nil {
			writeConfigError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ExperimentalResponse{Flags: onFlags(updated.Config.Experimental)})
		return
	}
}

func onFlags(stored map[string]bool) map[string]bool {
	out := make(map[string]bool, len(stored))
	for flag, on := range stored {
		if on {
			out[flag] = true
		}
	}
	return out
}
