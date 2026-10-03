package routing

import (
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func imageUnsupportedFailure(target provider.Target) canon.TurnFailed {
	return canon.TurnFailed{Failure: canon.Failure{
		Reason:  canon.FailInvalidRequest,
		Message: "routing: model " + string(target.Model) + " does not support image input",
	}}
}
