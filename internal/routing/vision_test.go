package routing

import "prism/internal/provider"

func targetWithImageInput(t provider.Target, enabled bool) provider.Target {
	t.ImageInput = enabled
	return t
}
