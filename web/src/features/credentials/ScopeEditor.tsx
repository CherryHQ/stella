import { useMemo, useState } from "react";
import { ChevronRight, Plus, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

function parseScopes(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((scope) => scope.trim())
    .filter(Boolean);
}

function groupOf(scope: string): string {
  const separator = scope.indexOf(":");
  return separator > 0 ? scope.slice(0, separator) : "other";
}

function dedupe(scopes: string[]): string[] {
  return Array.from(new Set(scopes));
}

// OAuth providers use different spellings for the scope that permits refresh
// token issuance. Lock only names declared by that provider's built-in list.
const REFRESH_TOKEN_SCOPES = new Set(["offline_access", "offline.access"]);

function refreshTokenScopes(defaults: string[]): string[] {
  return defaults.filter((scope) => REFRESH_TOKEN_SCOPES.has(scope));
}

// With no override, the built-in list is selected. Once an override exists,
// its checked state is authoritative while every built-in scope remains visible.
export function buildOAuthScopeDraft(saved: string[], defaults: string[]): string[] {
  const selected = saved.length > 0 ? saved : defaults;
  return dedupe([...selected, ...refreshTokenScopes(defaults)]);
}

export function ScopeEditor({
  value,
  defaults,
  onChange,
}: {
  value: string[];
  defaults: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [addDraft, setAddDraft] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const selected = useMemo(() => new Set(value), [value]);
  const defaultSet = useMemo(() => new Set(defaults), [defaults]);
  const requiredScopes = useMemo(() => refreshTokenScopes(defaults), [defaults]);
  const requiredSet = useMemo(() => new Set(requiredScopes), [requiredScopes]);
  const universe = useMemo(() => dedupe([...defaults, ...value]), [defaults, value]);

  const query = search.trim().toLowerCase();
  const groups = useMemo(() => {
    const grouped = new Map<string, string[]>();
    for (const scope of universe) {
      if (query && !scope.toLowerCase().includes(query)) continue;
      const group = groupOf(scope);
      grouped.set(group, [...(grouped.get(group) ?? []), scope]);
    }
    return Array.from(grouped.entries())
      .map(([name, scopes]) => ({ name, scopes: scopes.sort() }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [query, universe]);

  const setSelected = (next: string[]) => onChange(dedupe([...next, ...requiredScopes]));

  const toggle = (scope: string, checked: boolean) => {
    if (checked) setSelected([...value, scope]);
    else setSelected(value.filter((item) => item !== scope));
  };

  const toggleGroup = (scopes: string[], checked: boolean) => {
    if (checked) {
      setSelected([...value, ...scopes]);
      return;
    }
    const removed = new Set(scopes);
    setSelected(value.filter((scope) => !removed.has(scope) || requiredSet.has(scope)));
  };

  const addScopes = () => {
    const added = parseScopes(addDraft);
    if (added.length > 0) setSelected([...value, ...added]);
    setAddDraft("");
  };

  return (
    <div className="flex min-h-0 w-full flex-1 flex-col gap-3">
      <div className="flex flex-col gap-1">
        <span className="text-xs font-semibold text-muted-foreground">
          {t("credentials.oauth.scopes.title")}
        </span>
        <p className="text-xs text-muted-foreground">
          {t("credentials.oauth.scopes.selectionHint")}
        </p>
      </div>

      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          type="text"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t("credentials.oauth.scopes.search")}
          autoComplete="off"
          nativeInput
          className="pl-8"
        />
      </div>

      <div className="min-h-0 flex-1 space-y-1 overflow-y-auto">
        {groups.length === 0 && (
          <p className="px-1 py-3 text-center text-xs text-muted-foreground">
            {universe.length === 0
              ? t("credentials.oauth.scopes.empty")
              : t("credentials.oauth.scopes.noMatch")}
          </p>
        )}
        {groups.map((group) => {
          const open = query ? true : !collapsed.has(group.name);
          const checkedCount = group.scopes.filter((scope) => selected.has(scope)).length;
          const allChecked = checkedCount === group.scopes.length;
          return (
            <Collapsible
              key={group.name}
              open={open}
              onOpenChange={(next) =>
                setCollapsed((previous) => {
                  const copy = new Set(previous);
                  if (next) copy.delete(group.name);
                  else copy.add(group.name);
                  return copy;
                })
              }
            >
              <div className="flex items-center gap-2 px-1 py-1">
                <Checkbox
                  checked={allChecked}
                  indeterminate={!allChecked && checkedCount > 0}
                  onCheckedChange={(checked) => toggleGroup(group.scopes, checked === true)}
                  aria-label={group.name}
                />
                <CollapsibleTrigger className="flex flex-1 items-center gap-1.5 text-left text-xs font-medium text-foreground">
                  <ChevronRight
                    className={`size-3.5 text-muted-foreground transition-transform ${
                      open ? "rotate-90" : ""
                    }`}
                  />
                  <span>{group.name}</span>
                  <span className="text-muted-foreground">
                    {checkedCount}/{group.scopes.length}
                  </span>
                </CollapsibleTrigger>
              </div>
              <CollapsibleContent>
                <ul className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-x-4 gap-y-0.5 pb-1 pl-7">
                  {group.scopes.map((scope) => {
                    const required = requiredSet.has(scope);
                    const custom = !defaultSet.has(scope);
                    return (
                      <li key={scope} className="flex min-w-0 items-center gap-2 py-0.5">
                        <Checkbox
                          checked={selected.has(scope)}
                          disabled={required}
                          onCheckedChange={(checked) => toggle(scope, checked === true)}
                          aria-label={scope}
                        />
                        <span className="truncate font-mono text-xs text-foreground" title={scope}>
                          {scope}
                        </span>
                        {required && (
                          <Badge variant="info" size="sm">
                            {t("credentials.oauth.scopes.required")}
                          </Badge>
                        )}
                        {custom && (
                          <Badge variant="secondary" size="sm">
                            {t("credentials.oauth.scopes.custom")}
                          </Badge>
                        )}
                      </li>
                    );
                  })}
                </ul>
              </CollapsibleContent>
            </Collapsible>
          );
        })}
      </div>

      <div className="flex flex-col gap-1">
        <p className="text-xs text-muted-foreground">{t("credentials.oauth.scopes.addHint")}</p>
        <div className="flex items-start gap-2">
          <Textarea
            rows={1}
            value={addDraft}
            onChange={(event) => setAddDraft(event.target.value)}
            placeholder={t("credentials.oauth.scopes.addPlaceholder")}
            className="min-h-9 flex-1 font-mono text-xs"
          />
          <Button size="sm" variant="outline" onClick={addScopes} disabled={!addDraft.trim()}>
            <Plus className="size-3.5" />
            {t("credentials.oauth.scopes.add")}
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>
          {t("credentials.oauth.scopes.selectedCount", {
            selected: value.length,
            total: universe.length,
          })}
        </span>
        {requiredScopes.length > 0 && (
          <span>
            {t("credentials.oauth.scopes.requiredHint", { scopes: requiredScopes.join(", ") })}
          </span>
        )}
      </div>
    </div>
  );
}
