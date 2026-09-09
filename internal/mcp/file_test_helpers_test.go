package mcp

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCPClient struct {
	tools      []*mcpsdk.Tool
	listErr    error
	listFn     func(context.Context) ([]*mcpsdk.Tool, error)
	result     *mcpsdk.CallToolResult
	callErr    error
	callFn     func(context.Context, string, map[string]any) (*mcpsdk.CallToolResult, error)
	closeOnce  sync.Once
	closeCalls atomic.Int32
	closeCount atomic.Int32
}

func (c *fakeMCPClient) ListTools(ctx context.Context) ([]*mcpsdk.Tool, error) {
	if c.listFn != nil {
		return c.listFn(ctx)
	}
	return c.tools, c.listErr
}

func (c *fakeMCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	if c.callFn != nil {
		return c.callFn(ctx, name, args)
	}
	return c.result, c.callErr
}

func (c *fakeMCPClient) Close() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { c.closeCount.Add(1) })
	return nil
}

var contains = strings.Contains
