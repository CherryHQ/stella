import type { AgentsPageState } from "../agent-detail-state";
import { AgentToolsPanel } from "../AgentToolsPanel";

interface Props {
  state: AgentsPageState;
}

/** The agent editor's tools tab — the editor only opens for users who may write. */
export function ToolsTab({ state }: Props) {
  return <AgentToolsPanel agentId={state.editingId ?? ""} canEdit />;
}
