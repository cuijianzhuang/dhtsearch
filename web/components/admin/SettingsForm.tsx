"use client";

// The admin settings form. Renders every configuration item the Go
// backend's settings registry exposes, grouped; startup items arrive
// read-only, runtime items are editable. Saving goes through the
// /api/admin-action proxy (the token never reaches the browser) and
// the backend validates the whole batch atomically — the per-key
// errors it returns are shown under the offending fields.
//
// Value handling mirrors the registry's canonical forms: everything
// is a string; lists are edited one-per-line and joined with commas;
// secrets start empty and are only sent when typed into (an empty
// secret field means "leave unchanged", never "clear").

import { useMemo, useState, useSyncExternalStore } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import type { AdminSettingItem, AdminSettingsGroup } from "@/lib/admin";
import { THEME_PRESETS } from "@/lib/theme";

const APPEARANCE_LABELS: Record<string, string> = {
  system: "跟随系统",
  light: "浅色",
  dark: "深色",
};

/** The exact callback URLs the operator must register with the
 *  GitHub OAuth App and the OIDC provider. They are derived from the
 *  origin this page is served on — the same derivation the login
 *  flow itself uses — so what is shown here is what the providers
 *  will be called back on. */
function AuthCallbackInfo() {
  const origin = useSyncExternalStore(
    () => () => {},
    () => window.location.origin,
    () => ""
  );
  if (!origin) return null;
  const codeClass =
    "mt-1 block break-all rounded-md bg-soft px-2 py-1 font-mono text-xs text-ink-2";
  return (
    <div className="border-b border-line-2 px-4 py-3 text-sm leading-relaxed text-mute">
      <p>
        配置第三方登录时，把下面两个地址原样填到对应平台的回调 /
        重定向地址里：
      </p>
      <p className="mt-2">
        GitHub OAuth App 的 Authorization callback URL
        <code className={codeClass}>{origin}/api/auth/github/callback</code>
      </p>
      <p className="mt-2">
        OIDC 提供商的 Redirect URI（仅支持 OIDC，不支持 SAML）
        <code className={codeClass}>{origin}/api/auth/oidc/callback</code>
      </p>
    </div>
  );
}

function displayValue(item: AdminSettingItem): string {
  if (item.type === "list") {
    return item.value
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean)
      .join("\n");
  }
  return item.value;
}

function canonicalValue(item: AdminSettingItem, field: string): string {
  if (item.type === "list") {
    return field
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean)
      .join(",");
  }
  return field.trim();
}

function initFields(groups: AdminSettingsGroup[]) {
  const values: Record<string, string> = {};
  for (const g of groups) {
    for (const it of g.items) {
      if (it.editable && !it.sensitive) values[it.key] = displayValue(it);
    }
  }
  return values;
}

function SourceBadge({ source }: { source: AdminSettingItem["source"] }) {
  if (source === "db") {
    return (
      <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-[11px] font-medium text-accent-ink">
        数据库
      </span>
    );
  }
  return (
    <span className="rounded-md bg-soft px-1.5 py-0.5 text-[11px] text-mute">
      {source === "env" ? "环境变量" : "默认值"}
    </span>
  );
}

export default function SettingsForm({
  groups,
}: {
  groups: AdminSettingsGroup[];
}) {
  const router = useRouter();
  const items = useMemo(() => groups.flatMap((g) => g.items), [groups]);
  const itemByKey = useMemo(() => {
    const m = new Map<string, AdminSettingItem>();
    for (const it of items) m.set(it.key, it);
    return m;
  }, [items]);

  const [fields, setFields] = useState<Record<string, string>>(() =>
    initFields(groups)
  );
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [feedback, setFeedback] = useState<{
    applied: string[];
    restart: string[];
    warnings: string[];
  } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // After a save, router.refresh() delivers fresh props (new sources
  // and effective values); resync the fields from them while keeping
  // the feedback banner that explains what just happened. This is
  // React's "adjust state when a prop changes" pattern: the setState
  // calls run during render, before anything commits.
  const propSig = useMemo(() => JSON.stringify(groups), [groups]);
  const [seenSig, setSeenSig] = useState(propSig);
  if (seenSig !== propSig) {
    setSeenSig(propSig);
    setFields(initFields(groups));
    setSecrets({});
    setErrors({});
  }

  const dirtyValues = useMemo(() => {
    const out: Record<string, string | null> = {};
    for (const it of items) {
      if (!it.editable) continue;
      if (it.sensitive) {
        if (secrets[it.key]) out[it.key] = secrets[it.key];
        continue;
      }
      const cur = canonicalValue(it, fields[it.key] ?? "");
      if (cur !== it.value) out[it.key] = cur;
    }
    return out;
  }, [items, fields, secrets]);
  const dirtyCount = Object.keys(dirtyValues).length;

  async function post(values: Record<string, string | null>) {
    const res = await fetch("/api/admin-action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind: "save-settings", values }),
    });
    const data = (await res.json().catch(() => ({}))) as {
      applied?: string[];
      restart_required?: string[];
      warnings?: string[];
      errors?: Record<string, string>;
      error?: string;
    };
    return { status: res.status, data };
  }

  const labelOf = (key: string) => itemByKey.get(key)?.label ?? key;

  async function save() {
    if (dirtyCount === 0 || saving) return;
    setSaving(true);
    setErrors({});
    setFeedback(null);
    setNotice(null);
    try {
      const { status, data } = await post(dirtyValues);
      if (status === 200) {
        setFeedback({
          applied: (data.applied ?? []).map(labelOf),
          restart: (data.restart_required ?? []).map(labelOf),
          warnings: data.warnings ?? [],
        });
        router.refresh();
      } else if (status === 400 && data.errors) {
        setErrors(data.errors);
        setNotice("有设置项不合法，已全部不保存，请修正标红的项。");
      } else {
        setNotice(`保存失败：${data.error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("保存失败：无法连接到后端。");
    } finally {
      setSaving(false);
    }
  }

  async function resetOverride(item: AdminSettingItem) {
    if (saving) return;
    setSaving(true);
    setFeedback(null);
    setNotice(null);
    setErrors({});
    try {
      const { status, data } = await post({ [item.key]: null });
      if (status === 200) {
        setNotice(
          item.restart
            ? `「${item.label}」的数据库覆盖已删除，重启后回到环境/默认层。`
            : `「${item.label}」已恢复为环境/默认层的值。`
        );
        router.refresh();
      } else {
        setNotice(`恢复失败：${data.error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("恢复失败：无法连接到后端。");
    } finally {
      setSaving(false);
    }
  }

  function setField(key: string, v: string) {
    setFields((f) => ({ ...f, [key]: v }));
  }

  function renderControl(item: AdminSettingItem) {
    const err = errors[item.key];
    const fieldId = `setting-${item.key}`;
    if (!item.editable) {
      return (
        <p className="font-mono text-sm text-ink-2">
          {item.sensitive
            ? item.set
              ? "已设置（不显示明文）"
              : "未设置"
            : item.value || "（空）"}
        </p>
      );
    }
    if (item.type === "bool") {
      const on = (fields[item.key] ?? "false") === "true";
      return (
        <button
          type="button"
          role="switch"
          aria-checked={on}
          aria-label={item.label}
          onClick={() => setField(item.key, on ? "false" : "true")}
          className="inline-flex min-h-11 min-w-11 items-center justify-center"
        >
          <span
            className={`relative h-6 w-11 rounded-full transition-colors ${
              on ? "bg-accent" : "bg-input"
            }`}
          >
            <span
              className={`absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition-all ${
                on ? "left-[22px]" : "left-0.5"
              }`}
            />
          </span>
        </button>
      );
    }
    if (item.type === "secret") {
      return (
        <input
          id={fieldId}
          type="password"
          autoComplete="new-password"
          value={secrets[item.key] ?? ""}
          onChange={(e) =>
            setSecrets((s) => ({ ...s, [item.key]: e.target.value }))
          }
          placeholder={item.set ? "已设置，留空不改" : "未设置"}
          className="min-h-11 w-full min-w-56 rounded-lg border border-input bg-surface px-3 text-sm text-ink"
        />
      );
    }
    if (item.type === "enum" && item.key === "THEME_COLOR") {
      const cur = fields[item.key];
      return (
        <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={item.label}>
          {(item.options ?? []).map((opt) => {
            const preset = THEME_PRESETS[opt];
            if (!preset) return null;
            const selected = cur === opt;
            return (
              <button
                key={opt}
                type="button"
                role="radio"
                aria-checked={selected}
                onClick={() => setField(item.key, opt)}
                className={`flex min-h-11 flex-col items-center gap-1 rounded-lg border px-3 py-2 text-xs transition-colors ${
                  selected
                    ? "border-accent bg-accent-soft font-medium text-accent-ink"
                    : "border-input bg-surface text-mute hover:border-accent"
                }`}
              >
                <span
                  className="h-5 w-5 rounded-full border border-line"
                  style={{ backgroundColor: preset.pri }}
                />
                {preset.label}
              </button>
            );
          })}
        </div>
      );
    }
    if (item.type === "enum") {
      const cur = fields[item.key];
      return (
        <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={item.label}>
          {(item.options ?? []).map((opt) => (
            <button
              key={opt}
              type="button"
              role="radio"
              aria-checked={cur === opt}
              onClick={() => setField(item.key, opt)}
              className={`inline-flex min-h-11 items-center rounded-lg border px-3 text-sm transition-colors ${
                cur === opt
                  ? "border-accent bg-accent-soft font-medium text-accent-ink"
                  : "border-input bg-surface text-mute hover:border-accent hover:text-accent-ink"
              }`}
            >
              {APPEARANCE_LABELS[opt] ?? opt}
            </button>
          ))}
        </div>
      );
    }
    if (item.type === "list") {
      return (
        <textarea
          id={fieldId}
          value={fields[item.key] ?? ""}
          onChange={(e) => setField(item.key, e.target.value)}
          rows={Math.min(8, Math.max(3, (fields[item.key] ?? "").split("\n").length))}
          placeholder="每行一项"
          className="min-h-11 w-full min-w-56 rounded-lg border border-input bg-surface px-3 py-2 font-mono text-xs leading-relaxed text-ink"
        />
      );
    }
    const numeric =
      item.type === "int" || item.type === "int64" || item.type === "float";
    return (
      <input
        id={fieldId}
        type="text"
        inputMode={numeric ? "decimal" : undefined}
        value={fields[item.key] ?? ""}
        onChange={(e) => setField(item.key, e.target.value)}
        aria-invalid={err ? true : undefined}
        className="min-h-11 w-full min-w-56 rounded-lg border border-input bg-surface px-3 text-sm text-ink"
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">设置</h1>
        <p className="mt-1 text-sm text-mute">
          标「数据库」的项是管理端保存的覆盖值，优先于环境变量；改动按每项标注即时生效或需重启。启动配置只能查看，修改请编辑
          /etc/dhtsearch/dhtsearch.env 后重启服务。
        </p>
      </div>

      {feedback && (
        <div className="flex flex-col gap-2">
          {feedback.applied.length > 0 && (
            <p className="rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-sm text-accent-ink">
              已生效：{feedback.applied.join("、")}
            </p>
          )}
          {feedback.restart.length > 0 && (
            <p className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-warn-ink">
              已保存，重启后生效：{feedback.restart.join("、")}
            </p>
          )}
          {feedback.applied.length === 0 && feedback.restart.length === 0 && (
            <p className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-mute">
              没有实际变化。
            </p>
          )}
          {feedback.warnings.map((w) => (
            <p
              key={w}
              className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-warn-ink"
            >
              {w}
            </p>
          ))}
        </div>
      )}
      {notice && (
        <p className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-warn-ink">
          {notice}
        </p>
      )}

      {groups.map((group) => (
        <section
          key={group.key}
          className="rounded-xl border border-line bg-surface shadow-card"
        >
          <h2 className="border-b border-line-2 px-4 py-3 text-sm font-semibold text-ink">
            {group.label}
            {group.key === "service" && (
              <span className="ml-2 text-xs font-normal text-faint">
                只读 · 在 env 文件里改，重启生效
              </span>
            )}
          </h2>
          {group.key === "auth" && <AuthCallbackInfo />}
          {group.key === "moderation" ? (
            <div className="px-4 py-4">
              <p className="text-sm leading-relaxed text-mute">
                审核开关、模型、API Key、审核队列与最近记录已独立成专页，在那里统一修改，避免两处编辑同一批配置。
              </p>
              <Link
                href="/admin/llm"
                className="mt-3 inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover"
              >
                打开 LLM 审核页
              </Link>
            </div>
          ) : (
          <div className="divide-y divide-line-2">
            {group.items.map((item) => (
              <div
                key={item.key}
                className="grid gap-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4"
              >
                <div className="min-w-0">
                  <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-ink">
                    {item.label}
                    <SourceBadge source={item.source} />
                    {item.restart && item.editable && (
                      <span className="rounded-md border border-line px-1.5 py-0.5 text-[11px] text-warn-ink">
                        需重启
                      </span>
                    )}
                    {item.source === "db" && item.editable && (
                      <button
                        type="button"
                        onClick={() => resetOverride(item)}
                        disabled={saving}
                        className="text-[11px] text-mute underline decoration-dotted hover:text-accent-ink disabled:opacity-50"
                      >
                        恢复默认
                      </button>
                    )}
                  </p>
                  <p className="mt-0.5 font-mono text-[11px] text-faint">
                    {item.key}
                  </p>
                  {item.desc && (
                    <p className="mt-1 text-xs leading-relaxed text-mute">
                      {item.desc}
                    </p>
                  )}
                  {errors[item.key] && (
                    <p className="mt-1 text-xs text-warn-ink">
                      {errors[item.key]}
                    </p>
                  )}
                </div>
                <div className="sm:justify-self-end">{renderControl(item)}</div>
              </div>
            ))}
          </div>
          )}
        </section>
      ))}

      <div className="sticky bottom-4 flex items-center justify-end gap-3 rounded-xl border border-line bg-surface px-4 py-3 shadow-bar">
        <span className="text-sm text-mute">
          {dirtyCount > 0 ? `${dirtyCount} 项未保存` : "没有未保存的改动"}
        </span>
        <button
          type="button"
          onClick={save}
          disabled={dirtyCount === 0 || saving}
          className="inline-flex min-h-11 items-center rounded-lg bg-accent px-5 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
        >
          {saving ? "保存中…" : "保存设置"}
        </button>
      </div>
    </div>
  );
}
