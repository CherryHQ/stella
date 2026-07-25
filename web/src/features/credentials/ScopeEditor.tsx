import { useMemo, useState } from "react";
import { ChevronRight, Plus, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

// Split a pasted blob into scope tokens on newline / comma / whitespace,
// trimming and dropping empties. Dedupe is applied by the caller against the
// working set.
function parseScopes(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// Group a scope by its namespace prefix (text before the first ":"); scopes
// without a prefix fall into "other".
function groupOf(scope: string): string {
  const i = scope.indexOf(":");
  return i > 0 ? scope.slice(0, i) : "other";
}

function dedupe(list: string[]): string[] {
  return Array.from(new Set(list));
}

/**
 * ScopeEditor is the D6 admin scope editor: a grouped, searchable checklist
 * over the union of the working override and the built-in defaults, with a
 * bulk paste box, an edit-as-text projection, a diff bar, and an upgrade-drift
 * merge hint. The model is a single ordered string list; every view is a
 * projection of it.
 */
export function ScopeEditor({
  value,
  saved,
  defaults,
  onChange,
}: {
  value: string[];
  saved: string[];
  defaults: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [textMode, setTextMode] = useState(false);
  const [textDraft, setTextDraft] = useState("");
  const [addDraft, setAddDraft] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  const selected = useMemo(() => new Set(value), [value]);
  const defaultSet = useMemo(() => new Set(defaults), [defaults]);

  // Union of override and defaults: removed defaults stay visible (unchecked)
  // so deletion is reversible, not disappearance.
  const universe = useMemo(() => dedupe([...value, ...defaults]), [value, defaults]);

  const q = search.trim().toLowerCase();
  const groups = useMemo(() => {
    const byGroup = new Map<string, string[]>();
    for (const scope of universe) {
      if (q && !scope.toLowerCase().includes(q)) continue;
      const g = groupOf(scope);
      const arr = byGroup.get(g) ?? [];
      arr.push(scope);
      byGroup.set(g, arr);
    }
    return Array.from(byGroup.entries())
      .map(([name, scopes]) => ({ name, scopes: scopes.sort() }))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [universe, q]);

  const setSelected = (next: string[]) => onChange(dedupe(next));

  const toggle = (scope: string, on: boolean) => {
    if (on) setSelected([...value, scope]);
    else setSelected(value.filter((s) => s !== scope));
  };

  const toggleGroup = (scopes: string[], on: boolean) => {
    if (on) setSelected([...value, ...scopes]);
    else {
      const remove = new Set(scopes);
      setSelected(value.filter((s) => !remove.has(s)));
    }
  };

  const commitAdd = () => {
    const added = parseScopes(addDraft);
    if (added.length > 0) setSelected([...value, ...added]);
    setAddDraft("");
  };

  // Diff counts drive the persistent diff bar and consequence-labelled save.
  const savedSet = new Set(saved);
  const added = value.filter((s) => !savedSet.has(s)).length;
  const removed = saved.filter((s) => !selected.has(s)).length;
  const beyondDefault = value.filter((s) => !defaultSet.has(s)).length;

  // Upgrade drift: built-in defaults that the current override omits.
  const missingDefaults = defaults.filter((s) => !selected.has(s));

  const enterTextMode = () => {
    setTextDraft(value.join("\n"));
    setTextMode(true);
  };
  const applyTextDraft = (raw: string) => {
    setTextDraft(raw);
    setSelected(parseScopes(raw));
  };

  return (
    <div className="space-y-3">
      {missingDefaults.length > 0 && saved.length > 0 && (
        <div className="flex items-center justify-between gap-3 rounded-md border border-warning/36 bg-warning/8 px-3 py-2 text-xs">
          <span className="text-foreground">
            {t("credentials.oauth.scopes.mergeHint", { count: missingDefaults.length })}
          </span>
          <Button
            size="xs"
            variant="ghost"
            onClick={() => setSelected([...value, ...missingDefaults])}
          >
            {t("credentials.oauth.scopes.merge")}
          </Button>
        </div>
      )}

      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold text-muted-foreground">
          {t("credentials.oauth.scopes.title")}
        </span>
        <Button
          size="xs"
          variant="ghost"
          onClick={() => (textMode ? setTextMode(false) : enterTextMode())}
        >
          {textMode
            ? t("credentials.oauth.scopes.editAsList")
            : t("credentials.oauth.scopes.editAsText")}
        </Button>
      </div>

      {textMode ? (
        <Textarea
          rows={8}
          value={textDraft}
          onChange={(e) => applyTextDraft(e.target.value)}
          placeholder={t("credentials.oauth.scopes.textPlaceholder")}
          className="font-mono text-xs"
        />
      ) : (
        <>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t("credentials.oauth.scopes.search")}
              autoComplete="off"
              nativeInput
              className="pl-8"
            />
          </div>

          <div className="max-h-64 space-y-1 overflow-y-auto">
            {groups.length === 0 && (
              <p className="px-1 py-3 text-center text-xs text-muted-foreground">
                {universe.length === 0
                  ? t("credentials.oauth.scopes.empty")
                  : t("credentials.oauth.scopes.noMatch")}
              </p>
            )}
            {groups.map((group) => {
              const open = q ? true : !collapsed.has(group.name);
              const checkedCount = group.scopes.filter((s) => selected.has(s)).length;
              const allChecked = checkedCount === group.scopes.length;
              return (
                <Collapsible
                  key={group.name}
                  open={open}
                  onOpenChange={(next) =>
                    setCollapsed((prev) => {
                      const copy = new Set(prev);
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
                      onCheckedChange={(c) => toggleGroup(group.scopes, c === true)}
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
                    <ul className="space-y-0.5 pb-1 pl-7">
                      {group.scopes.map((scope) => {
                        const isCustom = !defaultSet.has(scope);
                        return (
                          <li key={scope} className="flex items-center gap-2 py-0.5">
                            <Checkbox
                              checked={selected.has(scope)}
                              onCheckedChange={(c) => toggle(scope, c === true)}
                            />
                            <span className="font-mono text-xs text-foreground">{scope}</span>
                            {isCustom && (
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

          <div className="flex items-start gap-2">
            <Textarea
              rows={1}
              value={addDraft}
              onChange={(e) => setAddDraft(e.target.value)}
              placeholder={t("credentials.oauth.scopes.addPlaceholder")}
              className="min-h-9 flex-1 font-mono text-xs"
            />
            <Button size="sm" variant="outline" onClick={commitAdd} disabled={!addDraft.trim()}>
              <Plus className="size-3.5" />
              {t("credentials.oauth.scopes.add")}
            </Button>
          </div>
        </>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        {added === 0 && removed === 0 ? (
          <span>{t("credentials.oauth.scopes.unchanged")}</span>
        ) : (
          <span className="text-foreground">
            {t("credentials.oauth.scopes.diffVsSaved", { added, removed })}
          </span>
        )}
        {beyondDefault > 0 && (
          <span>{t("credentials.oauth.scopes.diffVsDefault", { count: beyondDefault })}</span>
        )}
      </div>
    </div>
  );
}
