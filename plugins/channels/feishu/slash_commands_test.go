package feishu

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeSlashCommandAPI struct {
	existing  []string
	listErr   error
	createErr map[string]error
	created   []string
}

func (f *fakeSlashCommandAPI) List(context.Context) ([]string, error) {
	return f.existing, f.listErr
}

func (f *fakeSlashCommandAPI) Create(_ context.Context, command nativeSlashCommand) error {
	f.created = append(f.created, command.Name)
	return f.createErr[command.Name]
}

func TestRegisterNativeCommandsCreatesMissingAndPreservesExisting(t *testing.T) {
	api := &fakeSlashCommandAPI{existing: []string{"help", "start"}}
	(&Bot{slashCommands: api}).registerNativeCommands(t.Context())

	wantCreated := []string{"new", "compact", "abort", "whoami", "link", "doctor"}
	if !reflect.DeepEqual(api.created, wantCreated) {
		t.Fatalf("created = %v, want %v", api.created, wantCreated)
	}
}

func TestRegisterNativeCommandsStopsWhenListFails(t *testing.T) {
	api := &fakeSlashCommandAPI{listErr: errors.New("permission denied")}
	(&Bot{slashCommands: api}).registerNativeCommands(t.Context())
	if len(api.created) != 0 {
		t.Fatalf("writes after list failure: created=%v", api.created)
	}
}

func TestRegisterNativeCommandsContinuesAfterCreateFailure(t *testing.T) {
	api := &fakeSlashCommandAPI{createErr: map[string]error{"help": errors.New("permission denied")}}
	(&Bot{slashCommands: api}).registerNativeCommands(t.Context())
	want := []string{"help", "start", "new", "compact", "abort", "whoami", "link", "doctor"}
	if !reflect.DeepEqual(api.created, want) {
		t.Fatalf("created = %v, want %v", api.created, want)
	}
}

func TestSlashCommandBodyIncludesNameAndLocalizedDescription(t *testing.T) {
	body := slashCommandBody(feishuNativeSlashCommands[0])
	if body["command"] != "help" {
		t.Fatalf("command = %#v", body["command"])
	}
	description := body["description"].(map[string]any)
	if description["default_value"] == "" || description["i18n"].(map[string]string)["zh_cn"] == "" {
		t.Fatalf("description = %#v", description)
	}
}
