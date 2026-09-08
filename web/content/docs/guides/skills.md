---
title: Skills
---

Skills are reusable playbooks that teach Stella how to perform a task. A Skill
is a directory with a `SKILL.md` file and may include reference files or
scripts. Stella reads the files selected for the active Agent at the start of a
turn.

## Resource scopes and precedence

Stella reads four typed resource roots:

| Scope          | Files live in                | Applies to                |
| -------------- | ---------------------------- | ------------------------- |
| `system`       | deployment resources         | every user and Agent      |
| `system_agent` | one Agent's system resources | every user of that Agent  |
| `user`         | one user's resources         | all of that user's Agents |
| `user_agent`   | one user's Agent resources   | one user and one Agent    |

Project Skills under `.agents/skills/` are considered before package Skills.
For each package name, Stella selects the most specific complete package from
the four typed roots in this order:

```
user_agent > user > system_agent > system
```

A selected package replaces the complete package at broader scopes. Its files,
Skills, CLI entries, environment bindings, and MCP declarations are resolved as
one unit. An independent copy has no parent link and does not receive later
updates from the source. A standalone Skill follows the same scope precedence
by its name.

A `settings.json` file can disable a resource or add administrator-only
`forbidden` and `disabled_tools` entries. A disabled or malformed winner masks
an inherited resource. A narrower personal setting cannot override an
administrator prohibition.

Stella captures the resource files at turn admission. An edit made during a
turn takes effect on the next turn; reads in the current turn keep the captured
content. The capture is also the view used by search, prompt loading, CLI
preparation, and MCP discovery.

## Install and edit

Choose the destination before every install or upload. Stella does not infer a
scope from the conversation. The Web UI and API can create, copy, edit, and
delete a complete package or standalone Skill in a writable scope. Package
edits replace the complete package tree. File edits use the current content
digest when the API provides one, so a stale edit is reported as a conflict.
Direct writes from a shell are supported, but they do not provide a multi-file
transaction or a filesystem compare-and-swap guarantee.

An uploaded archive must contain one Skill directory with `SKILL.md`. A copied
package is a new resource under the destination owner. It keeps its own files,
settings, credentials, and OAuth grants; editing the source later does not
upgrade the copy.

## Use a Skill

Ask Stella to find an installed Skill, then load the one that matches the task.
`skill_installed_search` searches Skills visible to the active Agent; it does
not search a marketplace. `skill_load` reads the selected files into the
current session's temporary sandbox directory. The returned path is disposable
and is the path to use for the Skill's scripts and bundled files.

A failed preparation keeps the selected package masked for that turn. Stella
does not silently revive a lower-priority same-name resource. Native tools are
separate system capabilities and are not granted by a Skill or package with a
matching name.

## Update, disable, disconnect, and delete

To update a Skill or package, edit or replace its complete files in the
intended scope. Read the current resource before retrying after a digest
conflict. A successful write affects the next turn. The current turn finishes
with its captured Skill and package view.

Disabling a package or Skill blocks it from future selection. It does not
revoke an OAuth grant or remove resource bytes. Deleting an MCP declaration file
removes that declaration only. Use the MCP **Disconnect** action to revoke the
matching locally stored grant, close its connection, and block late callbacks or
refreshes; remote provider revocation is not guaranteed. The two operations are
independent.

Uninstalling a Skill or deleting a package removes it from future selection.
Stella keeps historical usage and changelog evidence needed by Skill reflection.
It does not run an online garbage collector for resource bytes or caches. Cleanup
requires an explicit maintenance action after the owning process and its
descendants are proven stopped.

The local backend enforces its configured sandbox policy but cannot prove that
detached descendants stopped. The `none` backend provides no reliable process
isolation. Do not treat a normal turn close as proof that descendants stopped,
and do not clear retained resource bytes using a time-to-live or a guessed
process ID. The `none` backend is unsuitable when cleanup requires a hard
process boundary.

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
invoke the Skill only after an explicit request; it does not disable the Skill
or change edit permissions.

For team workflows, commit a Project Skill under `.agents/skills/`. For a
shared or personal Skill, create or upload the complete directory in the
intended scope.

## Tips

Search before creating a new Skill. Keep one Skill focused on one workflow.
Load the Skill after creating or updating it and run a small representative
task before relying on it.
