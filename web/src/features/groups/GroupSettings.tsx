import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  updateGroup,
  addGroupMember,
  removeGroupMember,
  deleteGroup,
} from "@/lib/api-client/sdk.gen";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { groupMembersQueryOptions } from "@/lib/queries/groups";
import type { GroupMember } from "@/lib/api-client/types.gen";
import type { Agent } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
  DialogPanel,
} from "@/components/ui/dialog";
import { Field, FieldLabel } from "@/components/ui/field";

interface Props {
  groupId: string;
  groupName: string;
  open: boolean;
  onClose: () => void;
  onDeleted: () => void;
}

export function GroupSettings({ groupId, groupName, open, onClose, onDeleted }: Props) {
  const queryClient = useQueryClient();
  const { t } = useI18n();
  const { data: members = [] } = useQuery(groupMembersQueryOptions(groupId));
  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const [name, setName] = useState(groupName);
  const [saving, setSaving] = useState(false);

  const nonMemberAgents = agents.filter(
    (ag: Agent) => !members.some((m: GroupMember) => m.agent_id === ag.id),
  );

  const saveName = useCallback(async () => {
    if (!name.trim() || name === groupName) return;
    setSaving(true);
    try {
      await updateGroup({
        path: { groupId },
        body: { group_name: name.trim() },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["group", groupId] });
      await queryClient.invalidateQueries({ queryKey: ["groups"] });
    } finally {
      setSaving(false);
    }
  }, [name, groupName, groupId, queryClient]);

  const handleAddMember = useCallback(
    async (agentId: string) => {
      await addGroupMember({
        path: { groupId },
        body: { agent_id: agentId },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["group-members", groupId] });
    },
    [groupId, queryClient],
  );

  const handleRemoveMember = useCallback(
    async (agentId: string) => {
      if (members.length <= 1) return;
      await removeGroupMember({
        path: { groupId, agentId },
        throwOnError: true,
      });
      await queryClient.invalidateQueries({ queryKey: ["group-members", groupId] });
    },
    [groupId, members.length, queryClient],
  );

  const handleDelete = useCallback(async () => {
    if (!window.confirm(t("groups.deleteConfirm"))) return;
    await deleteGroup({ path: { groupId }, throwOnError: true });
    await queryClient.invalidateQueries({ queryKey: ["groups"] });
    onDeleted();
  }, [groupId, queryClient, onDeleted]);

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogPopup className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("groups.settingsTitle")}</DialogTitle>
          <DialogDescription>{t("groups.settingsDesc")}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="grid gap-5">
          <Field>
            <FieldLabel>{t("groups.groupName")}</FieldLabel>
            <div className="flex w-full gap-2">
              <Input
                value={name}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setName(e.target.value)}
                onKeyDown={(e: React.KeyboardEvent) => {
                  if (e.key === "Enter") void saveName();
                }}
              />
              <Button
                size="sm"
                disabled={!name.trim() || name === groupName || saving}
                onClick={() => void saveName()}
              >
                {saving ? t("common.saving") : t("common.save")}
              </Button>
            </div>
          </Field>

          <section className="grid gap-2">
            <h3 className="text-sm font-medium text-foreground">
              {t("groups.members", { count: members.length })}
            </h3>
            <div className="overflow-hidden rounded-xl border border-border bg-card">
              {members.map((m: GroupMember) => (
                <div
                  key={m.agent_id}
                  className="flex min-h-11 items-center justify-between gap-3 border-b border-border px-4 last:border-b-0"
                >
                  <span className="truncate text-sm font-medium text-foreground">
                    {m.agent_name || m.agent_id}
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => void handleRemoveMember(m.agent_id)}
                    disabled={members.length <= 1}
                  >
                    {t("common.remove")}
                  </Button>
                </div>
              ))}
            </div>
          </section>

          {nonMemberAgents.length > 0 && (
            <section className="grid gap-2">
              <h3 className="text-sm font-medium text-foreground">{t("groups.addAgent")}</h3>
              <div className="overflow-hidden rounded-xl border border-border bg-card">
                {nonMemberAgents.map((ag: Agent) => (
                  <button
                    key={ag.id}
                    type="button"
                    onClick={() => void handleAddMember(ag.id)}
                    className="flex min-h-11 w-full items-center gap-3 border-b border-border px-4 text-left text-sm text-muted-foreground transition-colors last:border-b-0 hover:bg-muted hover:text-foreground"
                  >
                    <span className="font-mono text-primary">+</span>
                    <span className="truncate font-medium">{ag.name}</span>
                  </button>
                ))}
              </div>
            </section>
          )}
        </DialogPanel>
        <DialogFooter>
          <div className="flex w-full items-center justify-between gap-3">
            <Button variant="destructive" size="sm" onClick={() => void handleDelete()}>
              {t("groups.deleteGroup")}
            </Button>
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t("common.close")}
            </Button>
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
