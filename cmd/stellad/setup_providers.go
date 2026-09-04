package main

import (
	"github.com/CherryHQ/stella/pkg/providers"
	anthropicprovider "github.com/CherryHQ/stella/plugins/providers/anthropic"
	openaiprovider "github.com/CherryHQ/stella/plugins/providers/openai"
	openairesponseprovider "github.com/CherryHQ/stella/plugins/providers/openai-response"
)

func setupProviderRegistry() (*providers.Registry, error) {
	return providers.NewRegistry(
		anthropicprovider.Definition(),
		openaiprovider.Definition(),
		openairesponseprovider.Definition(),
	)
}
