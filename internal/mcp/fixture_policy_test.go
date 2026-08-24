package mcp

import (
	"net"
	"os"
	"strconv"
	"testing"
)

func TestEndpointPolicyFromInheritedFDAllowsOnlyItsExactAuthority(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	payload, err := FixturePolicyDescriptor(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	policy, err := EndpointPolicyFromInheritedFD("" + fdString(reader))
	if err != nil {
		t.Fatalf("EndpointPolicyFromInheritedFD: %v", err)
	}
	defer func() { _ = reader.Close() }()

	want := "http://" + listener.Addr().String() + "/fixture"
	if err := validateEndpointURLWithPolicy(want, policy); err != nil {
		t.Fatalf("exact authority rejected: %v", err)
	}
	for _, raw := range []string{
		"https://" + listener.Addr().String() + "/fixture",
		"http://localhost:" + listener.Addr().String()[len("127.0.0.1:"):] + "/fixture",
		"http://127.0.0.1:1/fixture",
		"http://10.0.0.1/fixture",
	} {
		if err := validateEndpointURLWithPolicy(raw, policy); err == nil {
			t.Errorf("validateEndpointURLWithPolicy(%q) succeeded", raw)
		}
	}
}

func TestEndpointPolicyFromInheritedFDRejectsMalformedDescriptor(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(`{"version":1,"authority":"localhost:1234"}`); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if _, err := EndpointPolicyFromInheritedFD(fdString(reader)); err == nil {
		t.Fatal("malformed descriptor succeeded")
	}
	_ = reader.Close()
}

func fdString(file *os.File) string {
	return strconv.Itoa(int(file.Fd()))
}
