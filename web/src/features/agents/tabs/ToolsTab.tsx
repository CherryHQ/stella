import type { AgentsPageState } from "../agent-detail-state";
import { AgentToolsPanel } from "../AgentToolsPanel";

interface Props {
  state: AgentsPageState;
  /** False when the viewer may read the agent but not manage it. */
  canEdit: boolean;
}

/** The agent editor's tools tab. */
export function ToolsTab({ state, canEdit }: Props) {
  return <AgentToolsPanel agentId={state.editingId ?? ""} canEdit={canEdit} />;
}
