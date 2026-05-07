export interface Session {
  id: string;
  title: string;
  channel: string;
  agent_id: string;
  agent_name: string;
  user_id: number;
  user_name: string;
  last_active: string;
  archived: boolean;
}

export interface ToolBlock {
  type: "tool_call";
  id: string;
  name: string;
  arguments: Record<string, unknown>;
  status?: "running" | "done";
  result?: ToolResult;
}

export interface TextBlock {
  type: "text";
  text: string;
}

export interface ThinkingBlock {
  type: "thinking";
  thinking?: string;
  redacted?: boolean;
}

export type ContentBlock = TextBlock | ThinkingBlock | ToolBlock;

export interface ToolResult {
  tool_call_id: string;
  content: string;
  is_error: boolean;
}

export interface Message {
  role: "user" | "assistant" | "tool";
  content?: string;
  blocks?: ContentBlock[];
  tool_call_id?: string;
  timestamp: string;
  token_count?: number;
  model?: string;
  _streaming?: boolean;
}

export interface Agent {
  id: string;
  name: string;
}

export interface Tool {
  name: string;
  description: string;
  category: string;
  input_schema: Record<string, unknown>;
}

export interface Workspace {
  paths: string[];
}
