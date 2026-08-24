package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// TestbedFixtureFDEnv carries only the inherited descriptor number. The
// descriptor itself is anonymous and contains the versioned fixture authority.
// Production leaves this unset, so it retains the normal no-private-MCP policy.
const TestbedFixtureFDEnv = "STELLA_MCP_TESTBED_FIXTURE_FD"

const fixtureDescriptorLimit = 256

type fixtureDescriptor struct {
	Version   int    `json:"version"`
	Authority string `json:"authority"`
}

// EndpointPolicy is deliberately value-only: it never holds an open descriptor
// or a hostname. The testbed supervisor owns the listener; stellad receives
// one canonical IP-literal authority and must not discover anything else.
type EndpointPolicy struct {
	fixtureAuthority string
}

// EndpointPolicyFromInheritedFD reads one bounded, versioned descriptor and
// closes it immediately. An unset descriptor means the production policy. A
// configured but malformed descriptor is a startup error, never a fallback.
func EndpointPolicyFromInheritedFD(rawFD string) (EndpointPolicy, error) {
	if rawFD == "" {
		return EndpointPolicy{}, nil
	}
	fd, err := strconv.Atoi(rawFD)
	if err != nil || fd < 3 {
		return EndpointPolicy{}, errors.New("mcp: invalid testbed fixture descriptor")
	}
	file := os.NewFile(uintptr(fd), "mcp-testbed-fixture")
	if file == nil {
		return EndpointPolicy{}, errors.New("mcp: open testbed fixture descriptor")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, fixtureDescriptorLimit+1))
	if err != nil || len(data) > fixtureDescriptorLimit {
		return EndpointPolicy{}, errors.New("mcp: read testbed fixture descriptor")
	}
	var descriptor fixtureDescriptor
	if json.Unmarshal(data, &descriptor) != nil || descriptor.Version != 1 {
		return EndpointPolicy{}, errors.New("mcp: invalid testbed fixture descriptor")
	}
	address, err := netip.ParseAddrPort(descriptor.Authority)
	if err != nil || address.Addr() != netip.MustParseAddr("127.0.0.1") || address.Port() == 0 {
		return EndpointPolicy{}, errors.New("mcp: invalid testbed fixture authority")
	}
	canonical := "127.0.0.1:" + strconv.Itoa(int(address.Port()))
	if descriptor.Authority != canonical {
		return EndpointPolicy{}, errors.New("mcp: testbed fixture authority is not canonical")
	}
	return EndpointPolicy{fixtureAuthority: canonical}, nil
}

func (p EndpointPolicy) allowsAuthority(host, port string) bool {
	return p.fixtureAuthority != "" && host == "127.0.0.1" && port != "" && host+":"+port == p.fixtureAuthority
}

func (p EndpointPolicy) allowsURL(scheme, authority string) bool {
	return scheme == "http" && authority == p.fixtureAuthority && p.fixtureAuthority != ""
}

func (p EndpointPolicy) String() string {
	// Do not leak the testbed-only authority into ordinary diagnostics.
	if p.fixtureAuthority == "" {
		return ""
	}
	return "testbed-fixture"
}

func parseFixtureAuthority(raw string) (string, string, error) {
	address, err := netip.ParseAddrPort(raw)
	if err != nil {
		return "", "", err
	}
	return address.Addr().String(), strconv.Itoa(int(address.Port())), nil
}

// FixturePolicyDescriptor builds the only payload testbed may pass over the
// inherited descriptor. It is internal to this module, not a public API.
func FixturePolicyDescriptor(authority string) ([]byte, error) {
	// Kept package-private for the testbed contract test. The supervisor only
	// receives a canonical listener address and cannot smuggle another field.
	host, port, err := parseFixtureAuthority(authority)
	if err != nil || host != "127.0.0.1" || port == "0" || strings.Contains(authority, "[") {
		return nil, fmt.Errorf("invalid fixture authority")
	}
	return json.Marshal(fixtureDescriptor{Version: 1, Authority: authority})
}
