---
title: Skills
---

Skills are reusable playbooks that teach Stella how to perform a task. A Skill
is a directory with a `SKILL.md` file and may include reference files or
scripts. Stella can load a Skill when a task matches its description.

## Where Skills come from

Project Skills live in `.agents/skills/` in the current project. Personal and
managed Skills are installed from **Personal Settings > Skills** or, for an
administrator, **Admin Console > Deployment resources > Global Skills**. A
Skill that comes with an Agent Plugin follows that plugin's enabled state and
does not have a second Skill switch. Built-in Skills shipped with Stella are
part of the release.

When more than one visible Skill has the same name, Stella chooses one in this
order:

```
project > this agent > your Skills > shared agent > global > built-in
```

Stella applies the selected Skill's policy after choosing the winner. Disabling
the winner does not reveal a lower-priority Skill with the same name. Project
files are read when Stella captures the next turn, so an edit takes effect on
the next turn. A turn that has already started keeps its captured Skill view.
An administrator's **System** or **System · this agent** disable is an upper
limit. A narrower personal enable cannot override it.

## Install and configure

Choose the destination before every install or upload. Stella does not infer a
destination from the conversation.

- In an Agent's **Skills** page, choose **Mine · this agent** or, when you are
  an administrator, **System · this agent**.
- In **Personal Settings > Skills**, choose **Mine · all agents** or **Mine ·
  this agent**.
- In **Admin Console > Deployment resources > Global Skills**, choose
  **System · all agents** or **System · this agent**.

The Web UI can install from a remote source such as a skill name or GitHub
repository, or upload a ZIP file. An uploaded archive must contain one Skill
directory with `SKILL.md`. A package Skill is imported by an administrator from
the Plugins page and keeps the package's declared files and resource names.

After installing a package, configure its scope in the plugin settings. A
configuration can enable or disable the package, select declared CLI versions,
set an MCP endpoint, and start the required OAuth authorization flow. The
configuration page shows declared resources and whether visible endpoint or
OAuth client fields are present. It does not run the command, connect every
MCP server, or prove that a future turn can prepare all resources. The summary
is not a receipt for any particular execution. Open **Execution summary** below the turn's user message to
see, when recorded, the immutable package version and digest, config ID/scope/
revision, authorization/readiness, Skill winner states (`selected`, `masked`,
or `overridden`), and CLI requested/resolved versions with installation
evidence. An older cache or preinstalled image artifact can leave resolved or installation
evidence unknown. Turns recorded before this metadata existed have no
retroactive receipt; Stella does not reconstruct one from current configuration.

## Use a Skill

Ask Stella to find an installed Skill, then load the one that matches the task.
`skill_installed_search` searches Skills already visible to the active Agent;
it does not search a marketplace. `skill_load` reads the selected revision and
copies it into the current session's temporary sandbox directory. The returned
path is disposable and is the path to use for the Skill's scripts and bundled
files.

At the start of each turn, Stella selects the winning Skill and prepares the
resources selected by the same package configuration. This can include a
version-pinned CLI, environment bindings, an MCP tool directory, and required
account permissions. If a package's required authorization or CLI preparation
fails, that package's resources stay out of the turn. Stella records the
failure and does not silently switch to a lower-priority same-name resource.

The **Preview** action for a package update reports the candidate version,
content digest, resource names, OAuth changes, and scopes with incompatible
existing configurations. Preview only reads and validates the candidate. It
does not publish the package, install a CLI, connect an MCP server, or grant an
account permission. A real turn can still fail later if an account grant is
missing, a command cannot be installed in its sandbox, or a remote service is
unreachable.

## Update, disable, revoke, and uninstall

To update a managed Skill, replace its complete directory or ZIP and retry with
the version returned by the latest read. A version conflict means somebody
else changed it; read it again before choosing the next update. To update a
package, run **Preview** first, review the candidate and incompatible scopes,
then publish the update. New turns use the new package revision after a
successful publish. An admitted turn keeps its previous package and Skill
snapshot until it ends.

Disabling a plugin blocks that package from new turns. A normal plugin or Skill
configuration change lets an admitted turn finish. Setting a Skill's
`disable-model-invocation` to `true` disables automatic invocation of that Skill;
it does not disable the Skill itself or permission to edit it. A plugin disable
does not revoke an OAuth grant.

Use the owning account, assignment, OAuth, or Vault controls to revoke access.
Stella rejects new calls in the revoked scope, cancels or detaches matching
active work, and closes its runner. Files already copied into a sandbox and
side effects already made in an external service cannot be recalled.

Uninstalling a managed Skill removes it from future selection. Retiring a custom
package stops new selection and waits for active turns and other valid users
of its files to finish before deleting its package data. Cleanup can therefore
remain marked `cleanup_pending` after the database change. Stella keeps the
bytes when it cannot prove that a process and its descendants have stopped.
The local and `none` sandbox backends keep a recovery marker in this case; that
marker is never removed automatically and can block package and managed-Skill
cleanup across the deployment indefinitely, including after a normal turn
close. There is currently no product command or safe automated recovery path
that clears it. There is no time-to-live or process-ID guess that makes this
safe.

## Manage Skills from a conversation

When conversational Settings tools are enabled for the Agent, Stella can use
these tools for managed Skills:

- `settings_skill_list` and `settings_skill_get` read safe metadata, file names,
  and the current version. They never return file contents.
- `settings_skill_create` creates a Skill from a complete directory or ZIP at a
  sandbox path.
- `settings_skill_update` replaces the complete package and requires the
  version from `settings_skill_get`.
- `settings_skill_delete` removes the Skill and requires that same version.

These tools use the caller's normal ownership and Agent permissions. Remote
source installation, ZIP upload from the browser, plugin package import, and
credential binding stay on their Web UI or API surfaces. `skill_load` is a
runtime read and is separate from managed-Skill administration.

## Create a Skill

```markdown
---
name: my-deploy-script
description: Deploy the application to production.
---

# Deploy to production

1. Run the test suite.
2. Build the production bundle.
3. Verify the deployment is healthy.
```

`name` must use lowercase letters, numbers, and hyphens, and be at most 64
characters. `description` is required and appears in search results.
`disable-model-invocation` is optional. Set it to `true` when Stella should
invoke the Skill only after an explicit request; it does not turn the Skill off.

For team workflows, commit a Project Skill under `.agents/skills/`. For a
managed Skill, upload the complete directory or ZIP to the intended scope.

## Tips

Search before creating a new Skill. Keep one Skill focused on one workflow.
Load the Skill after creating or updating it and run a small representative
task before relying on it.
