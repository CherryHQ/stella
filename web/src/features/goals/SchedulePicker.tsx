import { useEffect, useRef, useState } from "react";
import { useI18n } from "@/lib/i18n";
import i18n from "@/lib/i18n/config";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export interface ScheduleValue {
  cron: string;
  every: string;
  at: string;
}

export const emptySchedule = (): ScheduleValue => ({ cron: "", every: "", at: "" });

/** A schedule is runnable when exactly one of its fields is set. */
export function isScheduleValid(v: ScheduleValue): boolean {
  return !!v.cron || !!v.every || !!v.at;
}

/** Parse a template's default_schedule string. Cron has spaces; a duration is bare ("6h"). */
export function scheduleFromString(s: string): ScheduleValue {
  if (!s) return emptySchedule();
  if (s.includes(" ")) return { ...emptySchedule(), cron: s };
  return { ...emptySchedule(), every: s };
}

type Freq = "once" | "daily" | "weekly" | "monthly" | "interval" | "advanced";

interface EditorState {
  freq: Freq;
  time: string; // "HH:MM"
  weekdays: number[]; // 0=Sun … 6=Sat
  dayOfMonth: number; // 1–31
  atLocal: string; // <input type="datetime-local"> value
  intervalN: number;
  intervalUnit: "m" | "h";
  cron: string; // advanced escape hatch
}

const DEFAULTS: EditorState = {
  freq: "daily",
  time: "09:00",
  weekdays: [1],
  dayOfMonth: 1,
  atLocal: "",
  intervalN: 30,
  intervalUnit: "m",
  cron: "",
};

const WEEKDAY_KEYS = [
  "schedule.weekdayShort.0",
  "schedule.weekdayShort.1",
  "schedule.weekdayShort.2",
  "schedule.weekdayShort.3",
  "schedule.weekdayShort.4",
  "schedule.weekdayShort.5",
  "schedule.weekdayShort.6",
] as const;

const SELECT_CLS =
  "h-8.5 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring";

function pad(n: number): string {
  return n.toString().padStart(2, "0");
}

function isoToLocalInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Derive editor state from a stored schedule. Unrecognized crons fall back to advanced. */
function parse(v: ScheduleValue): EditorState {
  if (v.at) return { ...DEFAULTS, freq: "once", atLocal: isoToLocalInput(v.at) };
  if (v.every) {
    const m = v.every.match(/^(\d+)(m|h)$/);
    if (m) {
      return {
        ...DEFAULTS,
        freq: "interval",
        intervalN: Number(m[1]),
        // SAFETY: the every regex anchors on the literal m|h suffixes, so group 2 is exactly one of them.
        intervalUnit: m[2] as "m" | "h",
      };
    }
    return { ...DEFAULTS, freq: "advanced", cron: "" };
  }
  if (v.cron) {
    const time = (min: string, hour: string) => `${pad(Number(hour))}:${pad(Number(min))}`;
    let m = v.cron.match(/^(\d+) (\d+) \* \* \*$/);
    if (m) return { ...DEFAULTS, freq: "daily", time: time(m[1], m[2]) };
    m = v.cron.match(/^(\d+) (\d+) \* \* ([\d,]+)$/);
    if (m) {
      return {
        ...DEFAULTS,
        freq: "weekly",
        time: time(m[1], m[2]),
        weekdays: m[3]
          .split(",")
          .map(Number)
          .filter((n) => n >= 0 && n <= 6),
      };
    }
    m = v.cron.match(/^(\d+) (\d+) (\d+) \* \*$/);
    if (m)
      return { ...DEFAULTS, freq: "monthly", time: time(m[1], m[2]), dayOfMonth: Number(m[3]) };
    return { ...DEFAULTS, freq: "advanced", cron: v.cron };
  }
  return DEFAULTS;
}

/** Build a stored schedule from editor state. Returns an empty (invalid) value while incomplete. */
function generate(s: EditorState): ScheduleValue {
  const [hh, mm] = s.time.split(":");
  const min = Number(mm);
  const hour = Number(hh);
  switch (s.freq) {
    case "once":
      return s.atLocal
        ? { ...emptySchedule(), at: new Date(s.atLocal).toISOString() }
        : emptySchedule();
    case "daily":
      return { ...emptySchedule(), cron: `${min} ${hour} * * *` };
    case "weekly":
      return s.weekdays.length
        ? {
            ...emptySchedule(),
            cron: `${min} ${hour} * * ${[...s.weekdays].sort((a, b) => a - b).join(",")}`,
          }
        : emptySchedule();
    case "monthly":
      return { ...emptySchedule(), cron: `${min} ${hour} ${s.dayOfMonth} * *` };
    case "interval":
      return s.intervalN > 0
        ? { ...emptySchedule(), every: `${s.intervalN}${s.intervalUnit}` }
        : emptySchedule();
    case "advanced":
      return s.cron.trim() ? { ...emptySchedule(), cron: s.cron.trim() } : emptySchedule();
  }
}

function eq(a: ScheduleValue, b: ScheduleValue): boolean {
  return a.cron === b.cron && a.every === b.every && a.at === b.at;
}

interface SchedulePickerProps {
  value: ScheduleValue;
  onChange: (v: ScheduleValue) => void;
}

/** Human-friendly recurrence builder that reads/writes {cron, every, at}; cron is hidden behind "advanced". */
export function SchedulePicker({ value, onChange }: SchedulePickerProps) {
  const { t } = useI18n();
  const [state, setState] = useState<EditorState>(() => parse(value));
  const lastEmitted = useRef<ScheduleValue>(value);

  // Re-sync when the parent changes value externally (edit open, template prefill).
  useEffect(() => {
    if (!eq(value, lastEmitted.current)) {
      setState(parse(value));
      lastEmitted.current = value;
    }
  }, [value]);

  // Seed the parent with the default schedule when mounted with an empty value,
  // so "every day at 09:00" is active immediately without requiring a click.
  useEffect(() => {
    if (!isScheduleValid(value)) {
      const v = generate(state);
      if (isScheduleValid(v)) {
        lastEmitted.current = v;
        onChange(v);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const update = (patch: Partial<EditorState>) => {
    const next = { ...state, ...patch };
    setState(next);
    const v = generate(next);
    lastEmitted.current = v;
    onChange(v);
  };
  // SAFETY: the select's options are the Freq values, so its value is a Freq.
  const onFreqChange = (e: React.ChangeEvent<HTMLSelectElement>) =>
    update({ freq: e.target.value as Freq });
  // SAFETY: the interval-unit select's options are m|h written by the editor.
  const onIntervalUnitChange = (e: React.ChangeEvent<HTMLSelectElement>) =>
    update({ intervalUnit: e.target.value as "m" | "h" });

  const toggleWeekday = (d: number) =>
    update({
      weekdays: state.weekdays.includes(d)
        ? state.weekdays.filter((x) => x !== d)
        : [...state.weekdays, d],
    });

  const sep = i18n.language.startsWith("zh") ? "、" : ", ";
  const weekdayName = (d: number) => t(WEEKDAY_KEYS[d]);
  const preview = (() => {
    const v = generate(state);
    if (!isScheduleValid(v)) return t("schedule.previewInvalid");
    let text: string;
    switch (state.freq) {
      case "once":
        text = t("schedule.onceAt", {
          time: new Date(state.atLocal).toLocaleString(i18n.language),
        });
        break;
      case "daily":
        text = t("schedule.dailyAt", { time: state.time });
        break;
      case "weekly":
        text = t("schedule.weeklyAt", {
          days: [...state.weekdays]
            .sort((a, b) => a - b)
            .map(weekdayName)
            .join(sep),
          time: state.time,
        });
        break;
      case "monthly":
        text = t("schedule.monthlyAt", { day: state.dayOfMonth, time: state.time });
        break;
      case "interval":
        text = t("schedule.everyText", {
          value: `${state.intervalN} ${t(state.intervalUnit === "h" ? "schedule.unitHours" : "schedule.unitMinutes")}`,
        });
        break;
      default:
        text = state.cron;
    }
    return t("schedule.preview", { text });
  })();

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-2">
        <label className="text-xs font-medium text-muted-foreground">
          {t("schedule.freqLabel")}
        </label>
        <select value={state.freq} onChange={onFreqChange} className={SELECT_CLS}>
          <option value="once">{t("schedule.freqOnce")}</option>
          <option value="daily">{t("schedule.freqDaily")}</option>
          <option value="weekly">{t("schedule.freqWeekly")}</option>
          <option value="monthly">{t("schedule.freqMonthly")}</option>
          <option value="interval">{t("schedule.freqInterval")}</option>
          <option value="advanced">{t("schedule.freqAdvanced")}</option>
        </select>
      </div>

      {state.freq === "once" && (
        <Field label={t("schedule.atLabel")}>
          <input
            type="datetime-local"
            value={state.atLocal}
            onChange={(e) => update({ atLocal: e.target.value })}
            className={SELECT_CLS}
          />
        </Field>
      )}

      {(state.freq === "daily" || state.freq === "weekly" || state.freq === "monthly") && (
        <Field label={t("schedule.timeLabel")}>
          <input
            type="time"
            value={state.time}
            onChange={(e) => update({ time: e.target.value })}
            className={SELECT_CLS}
          />
        </Field>
      )}

      {state.freq === "weekly" && (
        <Field label={t("schedule.weekdaysLabel")}>
          <div className="flex gap-1">
            {[0, 1, 2, 3, 4, 5, 6].map((d) => (
              <button
                key={d}
                type="button"
                onClick={() => toggleWeekday(d)}
                className={cn(
                  "grid size-8 place-items-center rounded-md border text-xs font-medium transition-colors",
                  state.weekdays.includes(d)
                    ? "border-primary bg-primary text-primary-foreground"
                    : "border-input text-muted-foreground hover:bg-muted/50",
                )}
              >
                {weekdayName(d)}
              </button>
            ))}
          </div>
        </Field>
      )}

      {state.freq === "monthly" && (
        <Field label={t("schedule.dayOfMonthLabel")}>
          <select
            value={state.dayOfMonth}
            onChange={(e) => update({ dayOfMonth: Number(e.target.value) })}
            className={SELECT_CLS}
          >
            {Array.from({ length: 31 }, (_, i) => i + 1).map((d) => (
              <option key={d} value={d}>
                {d}
              </option>
            ))}
          </select>
        </Field>
      )}

      {state.freq === "interval" && (
        <Field label={t("schedule.everyLabel")}>
          <div className="flex gap-2">
            <Input
              nativeInput
              type="number"
              min={1}
              value={state.intervalN}
              onChange={(e) => update({ intervalN: Math.max(1, Number(e.target.value) || 1) })}
              className="w-24"
            />
            <select
              value={state.intervalUnit}
              onChange={onIntervalUnitChange}
              className={SELECT_CLS}
            >
              <option value="m">{t("schedule.unitMinutes")}</option>
              <option value="h">{t("schedule.unitHours")}</option>
            </select>
          </div>
        </Field>
      )}

      {state.freq === "advanced" && (
        <Field label={t("schedule.cronLabel")}>
          <Input
            nativeInput
            type="text"
            value={state.cron}
            onChange={(e) => update({ cron: e.target.value })}
            placeholder="0 9 * * 1-5"
            className="font-mono"
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("schedule.cronHint")}</p>
        </Field>
      )}

      <p className="text-xs text-muted-foreground">{preview}</p>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="mb-1.5 block text-xs font-medium text-muted-foreground">{label}</label>
      {children}
    </div>
  );
}
