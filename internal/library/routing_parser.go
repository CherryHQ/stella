package library

import (
	"context"
	"fmt"
)

// RoutingParser is an immutable media-type dispatch table. Lifecycle code only
// depends on Parser and remains unaware of concrete document processors.
type RoutingParser struct {
	routes map[string]Parser
}

func NewRoutingParser(routes map[string]Parser) (*RoutingParser, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("library parser routes are required")
	}
	copyRoutes := make(map[string]Parser, len(routes))
	for mediaType, parser := range routes {
		if mediaType == "" || parser == nil {
			return nil, fmt.Errorf("library parser route requires a media type and parser")
		}
		copyRoutes[mediaType] = parser
	}
	return &RoutingParser{routes: copyRoutes}, nil
}

func (p *RoutingParser) parser(mediaType string) (Parser, error) {
	parser := p.routes[mediaType]
	if parser == nil {
		if isSupportedMediaType(mediaType) {
			return nil, fmt.Errorf("%w: parser for media type %q is unavailable", ErrServiceUnavailable, mediaType)
		}
		return nil, fmt.Errorf("%w: media type %q", ErrUnsupportedFileType, mediaType)
	}
	return parser, nil
}

func (p *RoutingParser) Profile(ctx context.Context, mediaType string) (string, error) {
	parser, err := p.parser(mediaType)
	if err != nil {
		return "", err
	}
	return parser.Profile(ctx, mediaType)
}

func (p *RoutingParser) FailureFence(ctx context.Context, mediaType string) (string, error) {
	parser, err := p.parser(mediaType)
	if err != nil {
		return "", err
	}
	return parserFailureFence(ctx, parser, mediaType)
}

func (p *RoutingParser) Parse(ctx context.Context, path, mediaType, expectedProfile string) ([]ParsedChunk, error) {
	parser, err := p.parser(mediaType)
	if err != nil {
		return nil, err
	}
	return parser.Parse(ctx, path, mediaType, expectedProfile)
}
