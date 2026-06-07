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
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
} from "@/components/ui/dialog";

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
      <DialogPopup>
        <DialogHeader>
          <DialogTitle>{t("groups.settingsTitle")}</DialogTitle>
          <DialogDescription>{t("groups.settingsDesc")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-5 py-2">
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("groups.groupName")}
            </label>
            <div className="flex gap-2">
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
                {saving ? "..." : t("common.save")}
              </Button>
            </div>
          </div>

          <div>
            <label className="mb-1 block text-xs text-muted-foreground">
              {t("groups.members", { count: members.length })}
            </label>
            <div className="grid gap-1 rounded-lg border border-border p-1.5">
              {members.map((m: GroupMember) => (
                <div
                  key={m.agent_id}
                  className="flex items-center justify-between rounded-md px-2 py-1.5"
                >
                  <span className="text-[13px] font-medium text-foreground">
                    {m.agent_name || m.agent_id}
                  </span>
                  <button
                    type="button"
                    onClick={() => void handleRemoveMember(m.agent_id)}
                    disabled={members.length <= 1}
                    className={cn(
                      "text-xs text-muted-foreground transition-colors",
                      members.length > 1
                        ? "hover:text-destructive cursor-pointer"
                        : "opacity-30 cursor-not-allowed",
                    )}
                  >
                    {t("common.remove")}
                  </button>
                </div>
              ))}
            </div>
          </div>

          {nonMemberAgents.length > 0 && (
            <div>
              <label className="mb-1 block text-xs text-muted-foreground">
                {t("groups.addAgent")}
              </label>
              <div className="grid gap-1 rounded-lg border border-border p-1.5">
                {nonMemberAgents.map((ag: Agent) => (
                  <button
                    key={ag.id}
                    type="button"
                    onClick={() => void handleAddMember(ag.id)}
                    className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <span className="text-xs text-primary">+</span>
                    <span className="font-medium">{ag.name}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
        <DialogFooter className="flex justify-between">
          <Button variant="destructive" size="sm" onClick={() => void handleDelete()}>
            {t("groups.deleteGroup")}
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
