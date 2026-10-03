package routing

import "github.com/deLiseLINO/prism/internal/provider"

func targetWithImageInput(t provider.Target, enabled bool) provider.Target {
	t.ImageInput = enabled
	return t
}
