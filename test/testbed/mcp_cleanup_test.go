package main

import "testing"

func TestFixtureInspectIgnoresFailedCommitBeforeSuccessfulRetry(t *testing.T) {
	fixture := &fixtureListener{routeKey: make([]byte, 32), routes: map[string]*fixtureRoute{}}
	route, err := fixture.routeForTrial("trial-retry")
	if err != nil {
		t.Fatal(err)
	}
	fixture.routes[route] = &fixtureRoute{id: "route-retry", entries: []fixtureLedgerEntry{
		{Method: "initialize"},
		{Method: "tools/list"},
		{Method: "tools/call", Tool: "lookup_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
		{Method: "tools/call", Tool: "transform_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
		{Method: "tools/call", Tool: "commit_brief", Outcome: "error"},
		{Method: "tools/call", Tool: "commit_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
	}}
	cleanup := &cleanupServer{fixture: fixture, leases: map[string]*cleanupLease{"lease": {trial: "trial-retry"}}}
	inspect, err := cleanup.inspect("lease")
	if err != nil {
		t.Fatal(err)
	}
	if !inspect.ChainComplete || inspect.AckWriteCount != 1 || inspect.DuplicateWriteCount != 0 {
		t.Fatalf("failed retry must not count as duplicate: %+v", inspect)
	}
}

func TestFixtureInspectRejectsWrongLeaseAndCountsDuplicateCommit(t *testing.T) {
	fixture := &fixtureListener{routeKey: make([]byte, 32), routes: map[string]*fixtureRoute{}}
	route, err := fixture.routeForTrial("trial-a")
	if err != nil {
		t.Fatal(err)
	}
	fixture.routes[route] = &fixtureRoute{id: "route-a", entries: []fixtureLedgerEntry{
		{RouteID: "route-a", Method: "initialize"},
		{RouteID: "route-a", Method: "tools/list"},
		{RouteID: "route-a", Method: "tools/call", Tool: "lookup_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
		{RouteID: "route-a", Method: "tools/call", Tool: "transform_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
		{RouteID: "route-a", Method: "tools/call", Tool: "commit_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
		{RouteID: "route-a", Method: "tools/call", Tool: "commit_brief", Outcome: "success", InputMatchesExpected: true, DependsOnPrevious: true},
	}}
	cleanup := &cleanupServer{fixture: fixture, leases: map[string]*cleanupLease{
		"lease-a": {trial: "trial-a", userID: "user-a", agentID: "agent-a", registrationID: "registration-a"},
	}}

	if _, err := cleanup.inspect("wrong-lease"); err == nil {
		t.Fatal("wrong lease was accepted")
	}
	inspect, err := cleanup.inspect("lease-a")
	if err != nil {
		t.Fatal(err)
	}
	if inspect.ChainComplete || inspect.AckWriteCount != 2 || inspect.DuplicateWriteCount != 1 {
		t.Fatalf("duplicate commit inspection = %+v", inspect)
	}
}
