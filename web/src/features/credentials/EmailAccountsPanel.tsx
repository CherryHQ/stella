import { useCallback, useEffect, useState } from "react";
import {
  deleteVaultEntry as deleteVaultEntryRequest,
  getVaultEntry,
  setVaultEntry,
} from "@/lib/api-client/sdk.gen";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/lib/i18n";

interface EmailAccount {
  imap_host: string;
  imap_port: number;
  imap_tls: string;
  smtp_host: string;
  smtp_port: number;
  smtp_tls: string;
  username: string;
  password?: string;
  from: string;
}

interface EmailConfig {
  default: string;
  accounts: Record<string, EmailAccount>;
}

interface EmailFormValues {
  name: string;
  imapHost: string;
  imapPort: string;
  imapTls: string;
  smtpHost: string;
  smtpPort: string;
  smtpTls: string;
  username: string;
  from: string;
  password: string;
}

const INITIAL_FORM: EmailFormValues = {
  name: "",
  imapHost: "",
  imapPort: "993",
  imapTls: "ssl",
  smtpHost: "",
  smtpPort: "587",
  smtpTls: "starttls",
  username: "",
  from: "",
  password: "",
};

export function EmailAccountsPanel({
  showToast,
}: {
  showToast: (msg: string, type?: "error") => void;
}) {
  const { t } = useI18n();
  const [config, setConfig] = useState<EmailConfig>({ default: "", accounts: {} });
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formOpen, setFormOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [form, setForm] = useState<EmailFormValues>(INITIAL_FORM);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const { data } = await getVaultEntry({
        path: { name: "EMAIL_CONFIG" },
        throwOnError: true,
      });
      if (data && data.value) {
        const parsed = JSON.parse(data.value) as EmailConfig;
        if (!parsed.accounts) parsed.accounts = {};
        setConfig(parsed);
      }
    } catch {
      setConfig({ default: "", accounts: {} });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const saveConfig = async (cfg: EmailConfig) => {
    await setVaultEntry({
      path: { name: "EMAIL_CONFIG" },
      body: { value: JSON.stringify(cfg) },
      throwOnError: true,
    });
    await load();
  };

  const handleSetDefault = async (name: string) => {
    setSaving(true);
    try {
      await saveConfig({ ...config, default: name });
      showToast(t("credentials.email.defaultUpdated"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("credentials.email.updateFailed"), "error");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (name: string) => {
    if (!window.confirm(t("credentials.email.deleteConfirm", { name }))) return;

    const updatedAccounts = { ...config.accounts };
    delete updatedAccounts[name];

    let nextDefault = config.default;
    if (nextDefault === name) {
      const keys = Object.keys(updatedAccounts);
      nextDefault = keys.length > 0 ? keys[0] : "";
    }

    setSaving(true);
    try {
      if (Object.keys(updatedAccounts).length === 0) {
        await deleteVaultEntryRequest({ path: { name: "EMAIL_CONFIG" }, throwOnError: true });
        setConfig({ default: "", accounts: {} });
      } else {
        await saveConfig({ default: nextDefault, accounts: updatedAccounts });
      }
      showToast(t("credentials.email.deleted"));
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("credentials.email.deleteFailed"), "error");
    } finally {
      setSaving(false);
    }
  };

  const handleEdit = (name: string) => {
    const acct = config.accounts[name];
    if (!acct) return;
    setForm({
      name,
      imapHost: acct.imap_host || "",
      imapPort: String(acct.imap_port || 993),
      imapTls: acct.imap_tls || "ssl",
      smtpHost: acct.smtp_host || "",
      smtpPort: String(acct.smtp_port || 587),
      smtpTls: acct.smtp_tls || "starttls",
      username: acct.username || "",
      from: acct.from || "",
      password: "",
    });
    setEditingName(name);
    setFormOpen(true);
    setErrors({});
  };

  const handleAdd = () => {
    setForm(INITIAL_FORM);
    setEditingName(null);
    setFormOpen(true);
    setErrors({});
  };

  const handleSave = async () => {
    const errs: Record<string, string> = {};
    if (!editingName) {
      if (!form.name) {
        errs.name = t("credentials.email.nameRequired");
      } else if (!/^[a-z][a-z0-9_]{0,31}$/.test(form.name)) {
        errs.name = t("credentials.email.nameFormat");
      } else if (config.accounts && form.name in config.accounts) {
        errs.name = t("credentials.email.nameExists");
      }
    }

    if (!form.imapHost) errs.imapHost = t("credentials.email.imapRequired");
    if (!form.smtpHost) errs.smtpHost = t("credentials.email.smtpRequired");
    if (!form.username) errs.username = t("credentials.email.usernameRequired");
    if (!form.from) errs.from = t("credentials.email.fromRequired");

    const imapPort = parseInt(form.imapPort, 10);
    if (isNaN(imapPort) || imapPort <= 0 || imapPort > 65535)
      errs.imapPort = t("credentials.email.invalidPort");

    const smtpPort = parseInt(form.smtpPort, 10);
    if (isNaN(smtpPort) || smtpPort <= 0 || smtpPort > 65535)
      errs.smtpPort = t("credentials.email.invalidPort");

    if (!editingName && !form.password) errs.password = t("credentials.email.passwordRequired");

    if (Object.keys(errs).length > 0) {
      setErrors(errs);
      return;
    }

    setSaving(true);
    try {
      const name = editingName || form.name;
      const existing = config.accounts?.[name];

      const account: EmailAccount = {
        imap_host: form.imapHost,
        imap_port: imapPort,
        imap_tls: form.imapTls,
        smtp_host: form.smtpHost,
        smtp_port: smtpPort,
        smtp_tls: form.smtpTls,
        username: form.username,
        from: form.from,
        password: form.password !== "" ? form.password : (existing?.password ?? ""),
      };

      const accounts = { ...config.accounts, [name]: account };
      let def = config.default;
      if (!def || Object.keys(config.accounts || {}).length === 0) def = name;

      await saveConfig({ default: def, accounts });
      showToast(editingName ? t("credentials.email.accountUpdated") : t("credentials.email.added"));
      setFormOpen(false);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t("credentials.email.saveFailed"), "error");
    } finally {
      setSaving(false);
    }
  };

  const hasAccounts = config.accounts && Object.keys(config.accounts).length > 0;

  return (
    <div className="space-y-6">
      {loading && <p className="text-sm text-muted-foreground">Loading…</p>}

      {hasAccounts && (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          {Object.entries(config.accounts).map(([name, acct]) => {
            const isDefault = config.default === name;
            return (
              <div
                key={name}
                className="min-w-0 rounded-xl border border-border bg-card p-5 flex flex-col justify-between"
              >
                <div>
                  <div className="flex min-w-0 items-start justify-between gap-3">
                    <div className="flex min-w-0 items-center gap-3">
                      <svg
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="2"
                        className="size-4 text-muted-foreground"
                      >
                        <rect width="20" height="16" x="2" y="4" rx="2" />
                        <path d="m22 7-8.97 5.7a1.94 1.94 0 0 1-2.06 0L2 7" />
                      </svg>
                      <span className="min-w-0 truncate text-sm font-medium font-mono">{name}</span>
                    </div>
                    {isDefault && <Badge variant="success">{t("credentials.email.default")}</Badge>}
                  </div>

                  <div className="mt-4 space-y-2 text-xs text-muted-foreground">
                    <div>
                      <span className="text-foreground font-medium">From:</span> {acct.from}
                    </div>
                    <div>
                      <span className="text-foreground font-medium">Username:</span> {acct.username}
                    </div>
                    <div>
                      <span className="text-foreground font-medium">IMAP:</span> {acct.imap_host}:
                      {acct.imap_port || 993} ({acct.imap_tls})
                    </div>
                    <div>
                      <span className="text-foreground font-medium">SMTP:</span> {acct.smtp_host}:
                      {acct.smtp_port || 587} ({acct.smtp_tls})
                    </div>
                  </div>
                </div>

                <div className="mt-5 flex items-center justify-end gap-2 border-t border-border pt-3">
                  {!isDefault && (
                    <Button
                      size="xs"
                      variant="ghost"
                      className="cursor-pointer duration-120"
                      onClick={() => handleSetDefault(name)}
                      loading={saving}
                    >
                      {t("credentials.email.setDefault")}
                    </Button>
                  )}
                  <Button
                    size="xs"
                    variant="outline"
                    className="cursor-pointer duration-120"
                    onClick={() => handleEdit(name)}
                  >
                    {t("common.edit")}
                  </Button>
                  <Button
                    size="xs"
                    variant="destructive-outline"
                    className="text-destructive hover:bg-destructive/10 cursor-pointer duration-120"
                    onClick={() => handleDelete(name)}
                    loading={saving}
                  >
                    {t("common.delete")}
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {!hasAccounts && !loading && (
        <p className="text-sm text-muted-foreground py-4 text-center">
          {t("credentials.email.noAccounts")}
        </p>
      )}

      {!formOpen ? (
        <div className="flex justify-end">
          <Button size="sm" onClick={handleAdd} className="cursor-pointer duration-120">
            {t("credentials.email.addAccount")}
          </Button>
        </div>
      ) : (
        <div className="rounded-xl border border-border bg-card p-6 space-y-6">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold text-foreground font-sans">
              {editingName
                ? t("credentials.email.editAccount", { name: editingName })
                : t("credentials.email.newAccount")}
            </h3>
            <Button
              size="xs"
              variant="ghost"
              className="cursor-pointer duration-120"
              onClick={() => setFormOpen(false)}
            >
              {t("common.cancel")}
            </Button>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {!editingName && (
              <div className="space-y-1.5 md:col-span-2">
                <label className="text-xs font-medium text-muted-foreground">
                  {t("credentials.email.accountName")}
                </label>
                <Input
                  type="text"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="e.g. personal, work"
                  autoComplete="off"
                  nativeInput
                />
                {errors.name && <p className="text-xs text-destructive">{errors.name}</p>}
              </div>
            )}

            {/* IMAP Config */}
            <div className="space-y-4 rounded-lg border border-border bg-muted p-4">
              <h4 className="font-mono text-[9px] text-muted-foreground">
                {t("credentials.email.imapIncoming")}
              </h4>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">
                  {t("credentials.email.imapHost")}
                </label>
                <Input
                  type="text"
                  value={form.imapHost}
                  onChange={(e) => setForm({ ...form, imapHost: e.target.value })}
                  placeholder="imap.example.com"
                  autoComplete="off"
                  nativeInput
                />
                {errors.imapHost && <p className="text-xs text-destructive">{errors.imapHost}</p>}
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.port")}
                  </label>
                  <Input
                    type="text"
                    value={form.imapPort}
                    onChange={(e) => setForm({ ...form, imapPort: e.target.value })}
                    placeholder="993"
                    autoComplete="off"
                    nativeInput
                  />
                  {errors.imapPort && <p className="text-xs text-destructive">{errors.imapPort}</p>}
                </div>

                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.tlsMode")}
                  </label>
                  <select
                    value={form.imapTls}
                    onChange={(e) => setForm({ ...form, imapTls: e.target.value })}
                    className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring h-9"
                  >
                    <option value="ssl">{t("credentials.email.sslRecommended")}</option>
                    <option value="starttls">{t("credentials.email.starttls")}</option>
                    <option value="none">{t("credentials.email.tlsNone")}</option>
                  </select>
                </div>
              </div>
            </div>

            {/* SMTP Config */}
            <div className="space-y-4 rounded-lg border border-border bg-muted p-4">
              <h4 className="font-mono text-[9px] text-muted-foreground">
                {t("credentials.email.smtpOutgoing")}
              </h4>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">
                  {t("credentials.email.smtpHost")}
                </label>
                <Input
                  type="text"
                  value={form.smtpHost}
                  onChange={(e) => setForm({ ...form, smtpHost: e.target.value })}
                  placeholder="smtp.example.com"
                  autoComplete="off"
                  nativeInput
                />
                {errors.smtpHost && <p className="text-xs text-destructive">{errors.smtpHost}</p>}
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.port")}
                  </label>
                  <Input
                    type="text"
                    value={form.smtpPort}
                    onChange={(e) => setForm({ ...form, smtpPort: e.target.value })}
                    placeholder="587"
                    autoComplete="off"
                    nativeInput
                  />
                  {errors.smtpPort && <p className="text-xs text-destructive">{errors.smtpPort}</p>}
                </div>

                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.tlsMode")}
                  </label>
                  <select
                    value={form.smtpTls}
                    onChange={(e) => setForm({ ...form, smtpTls: e.target.value })}
                    className="w-full rounded-lg border border-input bg-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring h-9"
                  >
                    <option value="starttls">{t("credentials.email.starttlsRecommended")}</option>
                    <option value="ssl">{t("credentials.email.sslRecommended")}</option>
                    <option value="none">{t("credentials.email.tlsNone")}</option>
                  </select>
                </div>
              </div>
            </div>

            {/* Credentials / Auth */}
            <div className="space-y-4 rounded-lg border border-border bg-muted p-4 md:col-span-2">
              <h4 className="font-mono text-[9px] text-muted-foreground">
                {t("credentials.email.credentials")}
              </h4>

              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.username")}
                  </label>
                  <Input
                    type="text"
                    value={form.username}
                    onChange={(e) => setForm({ ...form, username: e.target.value })}
                    placeholder="user@example.com"
                    autoComplete="off"
                    nativeInput
                  />
                  {errors.username && <p className="text-xs text-destructive">{errors.username}</p>}
                </div>

                <div className="space-y-1.5">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.fromAddress")}
                  </label>
                  <Input
                    type="text"
                    value={form.from}
                    onChange={(e) => setForm({ ...form, from: e.target.value })}
                    placeholder="Name <user@example.com> or user@example.com"
                    autoComplete="off"
                    nativeInput
                  />
                  {errors.from && <p className="text-xs text-destructive">{errors.from}</p>}
                </div>

                <div className="space-y-1.5 md:col-span-2">
                  <label className="text-xs font-medium text-muted-foreground">
                    {t("credentials.email.password")}
                  </label>
                  <Input
                    type="password"
                    value={form.password}
                    onChange={(e) => setForm({ ...form, password: e.target.value })}
                    placeholder={editingName ? t("credentials.email.keepExisting") : "password"}
                    autoComplete="new-password"
                    nativeInput
                  />
                  {errors.password && <p className="text-xs text-destructive">{errors.password}</p>}
                </div>
              </div>
            </div>
          </div>

          <div className="flex justify-end gap-3 pt-2">
            <Button
              size="sm"
              variant="ghost"
              className="cursor-pointer duration-120"
              onClick={() => setFormOpen(false)}
            >
              {t("common.cancel")}
            </Button>
            <Button
              size="sm"
              loading={saving}
              onClick={handleSave}
              className="cursor-pointer duration-120"
            >
              {t("credentials.email.saveAccount")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
