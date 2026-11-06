package daemon

import (
	"github.com/deLiseLINO/prism/internal/integrations"
)

// newIntegrationRegistry builds the full client registry on the local disk.
// Image generation models go to the omp integration only.
func newIntegrationRegistry(port int, env integrations.Env, home string, modelsSrc, imageModelsSrc func() []integrations.Model) (*integrations.Registry, *integrations.CodexIntegration, error) {
	registry := integrations.NewRegistry()
	models := integrations.DefaultPrismModels
	codexIntegration := integrations.NewCodex(integrations.CodexOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})
	if err := registry.Register(codexIntegration); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewGrok(integrations.GrokOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewOmp(integrations.OmpOptions{Port: port, Models: models, ModelsSource: modelsSrc, ImageModelsSource: imageModelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewClaude(integrations.ClaudeOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewPi(integrations.PiOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewOpencode(integrations.OpencodeOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	if err := registry.Register(integrations.NewHermes(integrations.HermesOptions{Port: port, Models: models, ModelsSource: modelsSrc, Env: env, Home: home})); err != nil {
		return nil, nil, err
	}
	return registry, codexIntegration, nil
}
