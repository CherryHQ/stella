package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/CherryHQ/stella/internal/library"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// deferredLibraryParser is unavailable until its concrete parser is activated.
// Activation is one-way: a profile that becomes durable can never change during
// this process lifetime.
type deferredLibraryParser struct {
	parser atomic.Pointer[libraryParserHolder]
}

type libraryParserHolder struct{ parser library.Parser }

func (p *deferredLibraryParser) Profile(mediaType string) (string, error) {
	holder := p.parser.Load()
	if holder == nil {
		return "", library.ErrServiceUnavailable
	}
	return holder.parser.Profile(mediaType)
}

func (p *deferredLibraryParser) Parse(ctx context.Context, path, mediaType string) ([]library.ParsedChunk, error) {
	holder := p.parser.Load()
	if holder == nil {
		return nil, library.ErrServiceUnavailable
	}
	return holder.parser.Parse(ctx, path, mediaType)
}

func (p *deferredLibraryParser) activate(parser library.Parser) bool {
	return parser != nil && p.parser.CompareAndSwap(nil, &libraryParserHolder{parser: parser})
}

func activateManagedXbergParser(ctx context.Context, parser *deferredLibraryParser, stellaHome string) error {
	if parser.parser.Load() != nil {
		return nil
	}
	bin := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	miseEnv := manifestplugins.RuntimeMiseEnv(stellaHome, "", "")
	// This process executes on the host, not inside an Agent sandbox. Keep all
	// mutable mise state under Stella Home and ignore the operator's mise config.
	miseTools := pkgsandbox.MiseToolsDir(stellaHome)
	miseEnv["MISE_CONFIG_DIR"] = filepath.Join(miseTools, "config")
	miseEnv["MISE_CACHE_DIR"] = filepath.Join(miseTools, "cache")
	miseEnv["MISE_STATE_DIR"] = filepath.Join(miseTools, "state")

	xberg, err := library.NewXbergCLIParserWithConfig(ctx, library.XbergCLIParserConfig{
		Binary: bin,
		Env:    envWithOverrides(os.Environ(), miseEnv),
	})
	if err != nil {
		return err
	}
	parser.activate(xberg)
	return nil
}

func envWithOverrides(env []string, overrides map[string]string) []string {
	result := append([]string(nil), env...)
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for i, entry := range result {
			if strings.HasPrefix(entry, prefix) {
				result[i] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, prefix+value)
		}
	}
	return result
}

var _ library.Parser = (*deferredLibraryParser)(nil)
