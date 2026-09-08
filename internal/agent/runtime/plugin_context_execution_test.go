package runtime_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin"
	skillpkg "github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestExecutionSummaryForTurnRetainsAdmissionEvidenceAndSkillPrecedence(t *testing.T) {
	db := dbtest.New(t)
	const userID = "10000000-0000-0000-0000-000000000094"
	definition := plugin.Definition{
		ID: "summary-runtime", DisplayName: "Summary runtime", Source: plugin.SourceBuiltin,
		DefaultEnabled: true, Revision: 1,
		Spec: publishedRuntimeSpec(t, `{"binaries":[{"name":"runner","tool":"github:owner/runner","version":">=1.2"}],"skills":[{"name":"shared"}]}`),
	}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	insertUser(t, db, userID)
	insertDefinition(t, db, definition)
	insertConfig(t, db, "20000000-0000-0000-0000-000000000094", definition, "user", userID, true, `{}`)
	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := agentruntime.NewPluginContext(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	failed := base.WithPreparationResult(pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{{
		PluginID: definition.ID, Reason: "CLI preparation failed", Ready: false,
	}}})
	packageDigest := "sha256:" + strings.Repeat("a", 64)
	packageRef := skillpkg.PackageSkillRef{PackageID: definition.ID, PackageDigest: packageDigest, Name: "shared"}
	managedDigest := strings.Repeat("b", 64)

	failedSummary := failed.ExecutionSummaryForTurn(nil)
	if failedSummary == nil {
		t.Fatal("failed package produced no execution summary")
	}
	failedPlugin := executionPlugin(t, failedSummary, definition.ID)
	if failedPlugin.Readiness != "unavailable" || !slices.Contains(failedPlugin.Failures, "CLI preparation failed") {
		t.Fatalf("failed plugin receipt = %+v, want bounded readiness failure", failedPlugin)
	}
	if len(failedPlugin.Binaries) != 1 || failedPlugin.Binaries[0].RequestedVersion != ">=1.2" || failedPlugin.Binaries[0].ResolvedVersion != "" {
		t.Fatalf("binary evidence = %+v, want requested range and unknown resolved version", failedPlugin.Binaries)
	}

	project, err := skillpkg.SnapshotProjectSkills(t.Context(), executionSummaryRoot{fsys: fstest.MapFS{
		".agents/skills/shared/SKILL.md": {Data: []byte("---\nname: shared\ndescription: project skill\n---\nproject\n")},
	}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	projectPackageRef := packageRef
	projectPackageRef.Masked = true
	projectView, err := skillpkg.NewSkillTurnView(project, nil, []skillpkg.PackageSkillRef{projectPackageRef}, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectSummary := base.ExecutionSummaryForTurn(&projectView)
	assertSkillState(t, projectSummary, "", "shared", "selected")
	assertSkillState(t, projectSummary, definition.ID, "shared", "overridden")

	managedView, err := skillpkg.NewSkillTurnView(nil, []skillpkg.ManagedSkillRef{{Identity: skillpkg.Skill{
		ID: "managed-shared", Scope: "user", Name: "shared", ContentDigest: managedDigest,
	}}}, []skillpkg.PackageSkillRef{packageRef}, nil)
	if err != nil {
		t.Fatal(err)
	}
	managedSummary := base.ExecutionSummaryForTurn(&managedView)
	assertSkillState(t, managedSummary, "", "shared", "selected")
	assertSkillState(t, managedSummary, definition.ID, "shared", "overridden")

	deniedView, err := skillpkg.CaptureSkillTurnView(t.Context(), executionSummaryIdentityReader{skills: []skillpkg.Skill{{
		ID: "denied-winner", Scope: "system_agent", AgentID: "agent-1", Name: "shared", ContentDigest: managedDigest,
	}}}, executionSummaryDenyAuthorizer{}, nil, []skillpkg.PackageSkillRef{packageRef}, skillpkg.ViewContext{})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillState(t, base.ExecutionSummaryForTurn(&deniedView), definition.ID, "shared", "masked")

	disabledView, err := skillpkg.NewSkillTurnView(nil, nil, []skillpkg.PackageSkillRef{{
		PackageID: definition.ID, PackageDigest: packageDigest, Name: "shared", Builtin: true,
	}}, []string{"system:shared"})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillState(t, base.ExecutionSummaryForTurn(&disabledView), definition.ID, "shared", "masked")

	notDisabledView, err := skillpkg.NewSkillTurnView(nil, nil, []skillpkg.PackageSkillRef{{
		PackageID: definition.ID, PackageDigest: packageDigest, Name: "shared", Builtin: true,
	}}, []string{"system_agent:shared"})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillState(t, base.ExecutionSummaryForTurn(&notDisabledView), definition.ID, "shared", "selected")
}

func executionPlugin(t *testing.T, summary *ai.ExecutionSummary, id string) ai.ExecutionPlugin {
	t.Helper()
	for _, item := range summary.Plugins {
		if item.PluginID == id {
			return item
		}
	}
	t.Fatalf("plugin %q missing from summary: %+v", id, summary.Plugins)
	return ai.ExecutionPlugin{}
}

func assertSkillState(t *testing.T, summary *ai.ExecutionSummary, pluginID, name, state string) {
	t.Helper()
	if summary == nil {
		t.Fatal("skill summary is nil")
	}
	for _, item := range summary.Skills {
		if item.PluginID == pluginID && item.Name == name && item.State == state {
			return
		}
	}
	t.Fatalf("skill %s/%s state %q missing from %+v", pluginID, name, state, summary.Skills)
}

type executionSummaryIdentityReader struct{ skills []skillpkg.Skill }

func (r executionSummaryIdentityReader) GetIdentity(context.Context, string) (*skillpkg.Skill, error) {
	return nil, errors.New("not implemented")
}

func (r executionSummaryIdentityReader) ListIdentityVisible(context.Context, skillpkg.ViewContext) ([]skillpkg.Skill, error) {
	return r.skills, nil
}

func (r executionSummaryIdentityReader) ListIdentityByScope(context.Context, string, string, string) ([]skillpkg.Skill, error) {
	return nil, nil
}

func (r executionSummaryIdentityReader) ListIdentityCandidate(context.Context, string, skillpkg.ViewContext) ([]skillpkg.Skill, error) {
	return nil, nil
}

func (r executionSummaryIdentityReader) LoadCurrentRevision(context.Context, skillpkg.Skill) (skillpkg.ManagedRevision, error) {
	return skillpkg.ManagedRevision{}, errors.New("not implemented")
}

func (r executionSummaryIdentityReader) LoadExactRevision(context.Context, skillpkg.Skill, string) (skillpkg.ManagedRevision, error) {
	return skillpkg.ManagedRevision{}, errors.New("not implemented")
}

type executionSummaryDenyAuthorizer struct{}

func (executionSummaryDenyAuthorizer) BeginRead(context.Context) (skillpkg.SkillReadDecision, error) {
	return executionSummaryDenyDecision{}, nil
}

type executionSummaryDenyDecision struct{}

func (executionSummaryDenyDecision) AllowRead(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

type executionSummaryRoot struct{ fsys fstest.MapFS }

func (r executionSummaryRoot) Close() error { return nil }
func (r executionSummaryRoot) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return fs.Stat(r.fsys, name)
}

func (r executionSummaryRoot) List(_ context.Context, name string, o home.ListOptions) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(r.fsys, name)
	if err != nil {
		return nil, err
	}
	if len(entries) > o.Limit {
		return nil, home.ErrListLimit
	}
	return entries, nil
}

func (r executionSummaryRoot) Read(_ context.Context, name string, dst io.Writer, o home.ReadOptions) error {
	b, err := fs.ReadFile(r.fsys, name)
	if err != nil {
		return err
	}
	if int64(len(b)) >= o.MaxBytes {
		return home.ErrReadLimit
	}
	_, err = dst.Write(b)
	return err
}

func (executionSummaryRoot) Write(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.ErrUnsupported
}

func (executionSummaryRoot) Upload(context.Context, string, io.Reader, home.WriteOptions) error {
	return errors.ErrUnsupported
}

func (executionSummaryRoot) Mkdir(context.Context, string, fs.FileMode, home.MkdirOptions) error {
	return errors.ErrUnsupported
}

func (executionSummaryRoot) Remove(context.Context, string, home.RemoveOptions) error {
	return errors.ErrUnsupported
}

func (executionSummaryRoot) Rename(context.Context, string, string, home.RenameOptions) error {
	return errors.ErrUnsupported
}
