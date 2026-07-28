import { useMemo, useState } from "react";
import { AlertTriangle, ChevronRight, Plus, Search } from "lucide-react";
import { Alert, AlertAction, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/lib/i18n";

// Split a pasted blob into scope tokens on newline / comma / whitespace,
// trimming and dropping empties. Dedupe is applied by the caller against the
// complete working set.
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

// Providers use different names for the scope that permits refresh-token
// issuance. Lock only names declared by that provider's built-in defaults.
const REFRESH_TOKEN_SCOPES = new Set(["offline_access", "offline.access"]);

function refreshTokenScopes(defaults: string[]): string[] {
  return defaults.filter((scope) => REFRESH_TOKEN_SCOPES.has(scope));
}

export function withRequiredOAuthScopes(value: string[], defaults: string[]): string[] {
  return dedupe([...value, ...refreshTokenScopes(defaults)]);
}

/**
 * ScopeEditor edits the complete requested scope set that an admin saves as a
 * replacement. Built-in scopes remain in the checklist after removal so the
 * consequence is visible. Lifecycle scopes required by a provider default are
 * retained in every editing mode.
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

  const requiredScopes = useMemo(() => refreshTokenScopes(defaults), [defaults]);
  const requiredSet = useMemo(() => new Set(requiredScopes), [requiredScopes]);
  const workingValue = useMemo(() => withRequiredOAuthScopes(value, defaults), [value, defaults]);
  const selected = useMemo(() => new Set(workingValue), [workingValue]);
  const defaultSet = useMemo(() => new Set(defaults), [defaults]);

  // Union of the complete working set and defaults: removed defaults stay
  // visible (unchecked) so deletion is reversible, not disappearance.
  const universe = useMemo(() => dedupe([...workingValue, ...defaults]), [workingValue, defaults]);

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

  const setSelected = (next: string[]) => onChange(dedupe([...next, ...requiredScopes]));

  const toggle = (scope: string, on: boolean) => {
    if (on) setSelected([...workingValue, scope]);
    else setSelected(workingValue.filter((s) => s !== scope));
  };

  const toggleGroup = (scopes: string[], on: boolean) => {
    if (on) setSelected([...workingValue, ...scopes]);
    else {
      const remove = new Set(scopes);
      setSelected(workingValue.filter((s) => !remove.has(s) || requiredSet.has(s)));
    }
  };

  const commitAdd = () => {
    const added = parseScopes(addDraft);
    if (added.length > 0) setSelected([...workingValue, ...added]);
    setAddDraft("");
  };

  // An empty stored override means the built-in defaults are the saved,
  // effective baseline. All diffs are set comparisons, so ordering is noise.
  const savedBaseline = saved.length > 0 ? saved : defaults;
  const savedSet = new Set(savedBaseline);
  const addedVsSaved = workingValue.filter((s) => !savedSet.has(s)).length;
  const removedVsSaved = savedBaseline.filter((s) => !selected.has(s)).length;
  const addedVsDefaults = workingValue.filter((s) => !defaultSet.has(s)).length;
  const removedDefaults = defaults.filter((s) => !selected.has(s));

  const enterTextMode = () => {
    setTextDraft(workingValue.join("\n"));
    setTextMode(true);
  };
  const exitTextMode = () => {
    setTextDraft(workingValue.join("\n"));
    setTextMode(false);
  };
  const applyTextDraft = (raw: string) => {
    // Keep separators while typing. The saved model is normalized immediately,
    // so a required lifecycle scope cannot be removed even if it is absent
    // from this transient text draft.
    setTextDraft(raw);
    setSelected(parseScopes(raw));
  };
  const restoreDefaults = () => {
    const next = dedupe([...workingValue, ...removedDefaults]);
    setSelected(next);
    if (textMode) setTextDraft(next.join("\n"));
  };

  return (
    <div className="flex min-h-0 w-full flex-1 flex-col gap-3">
      <div className="flex flex-col gap-1">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs font-semibold text-muted-foreground">
            {t("credentials.oauth.scopes.title")}
          </span>
          <Button
            size="xs"
            variant="ghost"
            onClick={() => (textMode ? exitTextMode() : enterTextMode())}
          >
            {textMode
              ? t("credentials.oauth.scopes.editAsList")
              : t("credentials.oauth.scopes.editAsText")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("credentials.oauth.scopes.replacementHint")}
        </p>
      </div>

      {removedDefaults.length > 0 && (
        <Alert variant="warning">
          <AlertTriangle />
          <AlertTitle>
            {t("credentials.oauth.scopes.defaultsRemoved", { count: removedDefaults.length })}
          </AlertTitle>
          <AlertDescription>{t("credentials.oauth.scopes.defaultsRemovedHint")}</AlertDescription>
          <AlertAction>
            <Button size="xs" variant="ghost" onClick={restoreDefaults}>
              {t("credentials.oauth.scopes.restoreDefaults")}
            </Button>
          </AlertAction>
        </Alert>
      )}

      {workingValue.length === 0 && defaults.length > 0 && (
        <Alert variant="warning">
          <AlertTriangle />
          <AlertTitle>{t("credentials.oauth.scopes.emptyOverride")}</AlertTitle>
          <AlertDescription>{t("credentials.oauth.scopes.emptyOverrideHint")}</AlertDescription>
        </Alert>
      )}

      {textMode ? (
        <Textarea
          rows={8}
          value={textDraft}
          onChange={(e) => applyTextDraft(e.target.value)}
          onBlur={() => setTextDraft(workingValue.join("\n"))}
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

          <div className="min-h-0 flex-1 space-y-1 overflow-y-auto">
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
                        const isRequired = requiredSet.has(scope);
                        const isCustom = !defaultSet.has(scope);
                        const isRemovedDefault = defaultSet.has(scope) && !selected.has(scope);
                        return (
                          <li key={scope} className="flex min-w-0 items-center gap-2 py-0.5">
                            <Checkbox
                              checked={selected.has(scope)}
                              disabled={isRequired}
                              onCheckedChange={(c) => toggle(scope, c === true)}
                              aria-label={scope}
                            />
                            <span
                              className="truncate font-mono text-xs text-foreground"
                              title={scope}
                            >
                              {scope}
                            </span>
                            {isRequired && (
                              <Badge variant="info" size="sm">
                                {t("credentials.oauth.scopes.required")}
                              </Badge>
                            )}
                            {isRemovedDefault && (
                              <Badge variant="warning" size="sm">
                                {t("credentials.oauth.scopes.removed")}
                              </Badge>
                            )}
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

          <div className="flex flex-col gap-1">
            <p className="text-xs text-muted-foreground">
              {t("credentials.oauth.scopes.bulkAddHint")}
            </p>
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
          </div>
        </>
      )}

      {requiredScopes.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t("credentials.oauth.scopes.requiredHint", { scopes: requiredScopes.join(", ") })}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>
          {t("credentials.oauth.scopes.diffVsSaved", {
            added: addedVsSaved,
            removed: removedVsSaved,
          })}
        </span>
        <span>
          {t("credentials.oauth.scopes.diffVsDefault", {
            added: addedVsDefaults,
            removed: removedDefaults.length,
          })}
        </span>
      </div>
    </div>
  );
}
