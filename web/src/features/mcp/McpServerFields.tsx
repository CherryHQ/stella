import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectItem,
  SelectPopup,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { McpServer } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";

export type McpTransport = McpServer["transport"];
export type McpAuthType = McpServer["auth_type"];

export function transportLabel(transport: McpTransport) {
  return transport === "streamable_http" ? "Streamable HTTP" : "SSE";
}

/**
 * The registration fields every MCP server form asks for — name, URL, transport,
 * auth. Shared so the settings inventory (`MCPServersPanel`) and the agent
 * profile's tools tab ask for a server in exactly the same words; each caller
 * still owns the destination (scope) question, which differs between them.
 */
export function McpServerFields({
  name,
  onNameChange,
  url,
  onUrlChange,
  transport,
  onTransportChange,
  authType,
  onAuthTypeChange,
  token,
  onTokenChange,
  editing,
}: {
  name: string;
  onNameChange: (value: string) => void;
  url: string;
  onUrlChange: (value: string) => void;
  transport: McpTransport;
  onTransportChange: (value: McpTransport) => void;
  authType: McpAuthType;
  onAuthTypeChange: (value: McpAuthType) => void;
  token: string;
  onTokenChange: (value: string) => void;
  /** An existing server keeps its stored token when the field is left blank. */
  editing: boolean;
}) {
  const { t } = useI18n();
  // SAFETY: the transport Select's options are the two McpTransport values (below).
  const onTransportChangeLocal = (value: string | null) =>
    value && onTransportChange(value as McpTransport);
  // SAFETY: the transport options render back through transportLabel which takes an McpTransport.
  const renderTransportLabel = (value: string) =>
    transportLabel((value as McpTransport) || transport);
  // SAFETY: the auth Select's options are the two McpAuthType values (below).
  const onAuthTypeChangeLocal = (value: string | null) =>
    value && onAuthTypeChange(value as McpAuthType);
  return (
    <>
      <Field>
        <FieldLabel>{t("mcp.name")}</FieldLabel>
        <Input
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
          placeholder="github"
          nativeInput
        />
        <FieldDescription>{t("mcp.name.description")}</FieldDescription>
      </Field>

      <Field>
        <FieldLabel>{t("mcp.url")}</FieldLabel>
        <Input
          value={url}
          onChange={(e) => onUrlChange(e.target.value)}
          placeholder="https://mcp.example.com/mcp"
          nativeInput
        />
      </Field>

      <Field>
        <FieldLabel>{t("mcp.transport")}</FieldLabel>
        <Select value={transport} onValueChange={onTransportChangeLocal}>
          <SelectTrigger>
            <SelectValue>{renderTransportLabel}</SelectValue>
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="streamable_http">Streamable HTTP</SelectItem>
            <SelectItem value="sse">SSE</SelectItem>
          </SelectPopup>
        </Select>
      </Field>

      <Field>
        <FieldLabel>{t("mcp.auth")}</FieldLabel>
        <Select value={authType} onValueChange={onAuthTypeChangeLocal}>
          <SelectTrigger>
            <SelectValue>
              {(value) => (value === "bearer" ? t("mcp.auth.bearer") : t("mcp.auth.none"))}
            </SelectValue>
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="none">{t("mcp.auth.none")}</SelectItem>
            <SelectItem value="bearer">{t("mcp.auth.bearer")}</SelectItem>
          </SelectPopup>
        </Select>
      </Field>

      {authType === "bearer" && (
        <Field>
          <FieldLabel>{t("mcp.token")}</FieldLabel>
          <Input
            type="password"
            value={token}
            onChange={(e) => onTokenChange(e.target.value)}
            autoComplete="off"
            nativeInput
          />
          <FieldDescription>
            {editing ? t("mcp.token.editDescription") : t("mcp.token.description")}
          </FieldDescription>
        </Field>
      )}
    </>
  );
}
