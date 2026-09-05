package server

import (
	"testing"

	pkgemail "github.com/CherryHQ/stella/pkg/email"
	pluginemail "github.com/CherryHQ/stella/plugins/email"
)

func TestEmailServicePresentRejectsTypedNil(t *testing.T) {
	var implementation *pluginemail.Service
	var service pkgemail.Service = implementation
	if emailServicePresent(service) {
		t.Fatal("typed-nil email service must remain a missing dependency")
	}
	if !emailServicePresent(pluginemail.NewService(nil, nil, nil)) {
		t.Fatal("constructed email service must satisfy the dependency")
	}
}
