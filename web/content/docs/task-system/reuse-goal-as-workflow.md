---
title: Reuse a goal as a scheduled workflow
---

Turn an accepted composite goal into a reusable workflow when you want the same plan to run again without asking the planner to recreate it.

## Before you start

You need an accepted composite goal. A workflow saved from that goal freezes the accepted decomposition plan, not the previous run's results.

Agents use the native `workflow` tool to save, list, inspect, and run workflows. Human operators manage saved workflows in the Web UI.

## Save the accepted goal

Ask Stella to save the goal with the `workflow` tool, or save the accepted goal from the Web UI. Give it a short name that describes the repeatable job.

The saved workflow is versioned. Editing a workflow later creates a new version; existing schedules keep using the exact version they were created with.

## Add inputs for changing text

Inputs are named placeholders such as `{{inputs.customer}}` or `{{inputs.date_range}}`. They can be substituted into titles, intents, and judgment prompts/rubrics.

Stella does not substitute inputs into executable check commands. If a required input is missing, the run fails before creating the new goal tree.

Input names and references are checked at save time: a name must use only letters, digits, `_` or `-`, and every `{{inputs.name}}` referenced by the plan must be a declared input.

## Run it manually

Ask Stella to run the workflow with the required inputs, or run it from the Web UI. Each run creates a fresh root goal. The original accepted goal stays closed.

## Schedule it daily

Ask Stella to schedule the workflow with the required inputs and cadence. For example: “save this goal as a workflow and run it every morning.” Stella should save the goal first, then create a workflow scheduler job. In the Web UI, the workflow detail page has a Schedule button that does the same thing.

Cron schedules use the server's local time in v1.

## What `fully_frozen` means

A `fully_frozen` workflow has a saved plan for every composite node. Scheduled workflow jobs require this by default, so the same structure is replayed every time without planner drift.

Partially frozen workflows can leave some sub-plans open for live replanning. Scheduling those requires an explicit allow-replan opt-in.

## Overlap behavior

The scheduler skips only when the previous workflow run completed instantiation and its root goal is still active. Failed instantiation does not block the next tick; a stalled instantiation is resumed instead of duplicated.
