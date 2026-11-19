"use client";

// The LLM moderation console: everything about the automatic review
// pipeline on one page — live queue numbers, the moderation settings
// (the same registry items the settings page used to render inline,
// saved through the same /api/admin-action proxy), a connection probe
// that really calls the configured endpoint once, and the most recent
// verdicts from the backend's moderation audit log.

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import type {
  AdminModeration,
  AdminSettingItem,
  ModerationRecentItem,
} from "@/lib/admin";
import { formatCount, formatRelativeTime } from "@/lib/format";

const KEY_ENABLED = "MODERATION_ENABLED";
const KEY_BASE_URL = "OPENAI_BASE_URL";
const KEY_API_KEY = "OPENAI_API_KEY";
const KEY_MODEL = "OPENAI_MODEL";
const KEY_DRY_RUN = "MODERATION_DRY_RUN";

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

const LABEL_TEXT: Record<string, string> = {
  ok: "通过",
  adult: "成人",
  spam: "垃圾",
};

const ACTION_TEXT: Record<string, string> = {
  kept: "保留",
  deleted: "已删除",
  would_delete: "试运行·将删除",
};

type ProbeResult =
  | { state: "ok"; model: string; latencyMs: number }
  | { state: "fail"; error: string };

export default function LlmConsole({
  items,
  data,
}: {
  items: AdminSettingItem[];
  data: AdminModeration;
}) {
  const router = useRouter();
  const itemByKey = useMemo(() => {
    const m = new Map<string, AdminSettingItem>();
    for (const it of items) m.set(it.key, it);
    return m;
  }, [items]);

  const initFields = () => {
    const values: Record<string, string> = {};
    for (const it of items) {
      if (it.editable && !it.sensitive) values[it.key] = it.value;
    }
    return values;
  };
  const [fields, setFields] = useState<Record<string, string>>(initFields);
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [feedback, setFeedback] = useState<{
    applied: string[];
    restart: string[];
  } | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // Resync from fresh props after a save (router.refresh), same
  // render-time adjustment pattern as SettingsForm.
  const propSig = useMemo(() => JSON.stringify(items), [items]);
  const [seenSig, setSeenSig] = useState(propSig);
  if (seenSig !== propSig) {
    setSeenSig(propSig);
    setFields(initFields());
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
      const cur = (fields[it.key] ?? "").trim();
      if (cur !== it.value) out[it.key] = cur;
    }
    return out;
  }, [items, fields, secrets]);
  const dirtyCount = Object.keys(dirtyValues).length;

  const labelOf = (key: string) => itemByKey.get(key)?.label ?? key;

  async function save() {
    if (dirtyCount === 0 || saving) return;
    setSaving(true);
    setErrors({});
    setFeedback(null);
    setNotice(null);
    try {
      const res = await fetch("/api/admin-action", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind: "save-settings", values: dirtyValues }),
      });
      const body = (await res.json().catch(() => ({}))) as {
        applied?: string[];
        restart_required?: string[];
        errors?: Record<string, string>;
        error?: string;
      };
      if (res.status === 200) {
        setFeedback({
          applied: (body.applied ?? []).map(labelOf),
          restart: (body.restart_required ?? []).map(labelOf),
        });
        router.refresh();
      } else if (res.status === 400 && body.errors) {
        setErrors(body.errors);
        setNotice("有设置项不合法，已全部不保存，请修正标红的项。");
      } else {
        setNotice(`保存失败：${body.error ?? `HTTP ${res.status}`}`);
      }
    } catch {
      setNotice("保存失败：无法连接到后端。");
    } finally {
      setSaving(false);
    }
  }

  // --- Connection probe ---
  const [probing, setProbing] = useState(false);
  const [probe, setProbe] = useState<ProbeResult | null>(null);
  const [cooldown, setCooldown] = useState(false);

  async function testConnection() {
    if (probing || cooldown) return;
    setProbing(true);
    setProbe(null);
    try {
      const res = await fetch("/api/admin-action", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: "test-llm",
          // Unsaved edits are probed too: pass the current field
          // values; empty means "use the effective setting".
          base_url:
            dirtyValues[KEY_BASE_URL] != null
              ? (fields[KEY_BASE_URL] ?? "")
              : "",
          model:
            dirtyValues[KEY_MODEL] != null ? (fields[KEY_MODEL] ?? "") : "",
          api_key: secrets[KEY_API_KEY] ?? "",
        }),
      });
      const body = (await res.json().catch(() => ({}))) as {
        ok?: boolean;
        model?: string;
        latency_ms?: number;
        error?: string;
      };
      if (res.status === 429) {
        setProbe({ state: "fail", error: "点击太频繁，请等几秒再试。" });
      } else if (body.ok) {
        setProbe({
          state: "ok",
          model: body.model ?? "",
          latencyMs: body.latency_ms ?? 0,
        });
      } else {
        setProbe({
          state: "fail",
          error: body.error ?? `HTTP ${res.status}`,
        });
      }
    } catch {
      setProbe({ state: "fail", error: "无法连接到后端。" });
    } finally {
      setProbing(false);
      // Mirror the backend debounce so the button cannot be hammered.
      setCooldown(true);
      setTimeout(() => setCooldown(false), 5000);
    }
  }

  function setField(key: string, v: string) {
    setFields((f) => ({ ...f, [key]: v }));
  }

  function renderControl(item: AdminSettingItem) {
    const err = errors[item.key];
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
    return (
      <input
        type="text"
        value={fields[item.key] ?? ""}
        onChange={(e) => setField(item.key, e.target.value)}
        aria-invalid={err ? true : undefined}
        className="min-h-11 w-full min-w-56 rounded-lg border border-input bg-surface px-3 text-sm text-ink"
      />
    );
  }

  const enabledItem = itemByKey.get(KEY_ENABLED);
  const enabledOn = enabledItem
    ? (fields[KEY_ENABLED] ?? enabledItem.value) === "true"
    : data.enabled;
  const dryRunItem = itemByKey.get(KEY_DRY_RUN);
  const dryRunOn = dryRunItem
    ? (fields[KEY_DRY_RUN] ?? dryRunItem.value) === "true"
    : data.dry_run;

  const counts = [
    { label: "待审核", value: data.pending },
    { label: "已审核", value: data.stats.reviewed },
    { label: "成人删除", value: data.stats.adult_removed },
    { label: "垃圾删除", value: data.stats.spam_removed },
    { label: "标题去广告", value: data.stats.titles_trimmed },
    { label: "审核出错", value: data.stats.errors },
  ];

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-lg font-semibold text-ink">LLM 审核</h1>
        <p className="mt-1 text-sm text-mute">
          新入库的种子先过规则过滤，再由大模型复核标题：判为成人/垃圾的自动删除并拉黑
          infohash，通过的标记已审核。试运行模式下只记录结论、不真删。
        </p>
      </div>

      {/* Live status + queue numbers */}
      <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className={`rounded-md px-2 py-1 text-xs font-medium ${
              enabledOn
                ? "bg-accent-soft text-accent-ink"
                : "bg-soft text-mute"
            }`}
          >
            {enabledOn ? "审核已启用" : "审核未启用"}
          </span>
          <span className="rounded-md bg-soft px-2 py-1 text-xs text-mute">
            {data.key_set ? "API Key 已设置" : "API Key 未设置"}
          </span>
          <span className="rounded-md bg-soft px-2 py-1 text-xs text-mute">
            {dryRunOn ? "试运行（只标记不删除）" : "正式模式（自动删除）"}
          </span>
          <span className="ml-auto font-mono text-xs text-faint">
            {data.model}
          </span>
        </div>
        <div className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
          {counts.map((c) => (
            <div key={c.label} className="rounded-lg bg-soft px-3 py-2">
              <p className="text-[11px] text-mute">{c.label}</p>
              <p className="mt-1 text-lg font-semibold tabular-nums text-ink">
                {formatCount(c.value)}
              </p>
            </div>
          ))}
        </div>
      </section>

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
        </div>
      )}
      {notice && (
        <p className="rounded-lg border border-line bg-soft px-4 py-3 text-sm text-warn-ink">
          {notice}
        </p>
      )}

      {/* Moderation settings (same registry items, dedicated page) */}
      <section className="rounded-xl border border-line bg-surface shadow-card">
        <h2 className="border-b border-line-2 px-4 py-3 text-sm font-semibold text-ink">
          审核设置
          <span className="ml-2 text-xs font-normal text-faint">
            与设置页同一份配置，数据库覆盖优先于环境变量
          </span>
        </h2>
        <div className="divide-y divide-line-2">
          {items.map((item) => (
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
        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-line-2 px-4 py-3">
          <span className="text-sm text-mute">
            {dirtyCount > 0 ? `${dirtyCount} 项未保存` : "没有未保存的改动"}
          </span>
          <button
            type="button"
            onClick={testConnection}
            disabled={probing || cooldown}
            className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:cursor-not-allowed disabled:opacity-50"
          >
            {probing ? "测试中…" : "测试连接"}
          </button>
          <button
            type="button"
            onClick={save}
            disabled={dirtyCount === 0 || saving}
            className="inline-flex min-h-11 items-center rounded-lg bg-accent px-5 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
          >
            {saving ? "保存中…" : "保存设置"}
          </button>
        </div>
        {probe && (
          <p
            className={`border-t border-line-2 px-4 py-3 text-sm ${
              probe.state === "ok" ? "text-accent-ink" : "text-warn-ink"
            }`}
          >
            {probe.state === "ok"
              ? `连接成功：模型 ${probe.model} 可用，延迟 ${probe.latencyMs} ms。`
              : `连接失败：${probe.error}`}
          </p>
        )}
      </section>

      {/* Recent verdicts */}
      <section className="rounded-xl border border-line bg-surface shadow-card">
        <h2 className="border-b border-line-2 px-4 py-3 text-sm font-semibold text-ink">
          最近审核记录
          <span className="ml-2 text-xs font-normal text-faint">
            保留最近 1000 条
          </span>
        </h2>
        {data.recent.length === 0 ? (
          <p className="px-4 py-6 text-sm text-mute">
            暂无记录。审核跑过一轮后，每条结论（通过/成人/垃圾与处理结果）都会记在这里。
          </p>
        ) : (
          <ul className="divide-y divide-line-2">
            {data.recent.map((r: ModerationRecentItem) => (
              <li
                key={`${r.info_hash}-${r.created_at}`}
                className="flex items-center gap-3 px-4 py-2.5"
              >
                <span
                  className={`shrink-0 rounded-md px-1.5 py-0.5 text-[11px] font-medium ${
                    r.label === "ok"
                      ? "bg-accent-soft text-accent-ink"
                      : "bg-soft text-warn-ink"
                  }`}
                >
                  {LABEL_TEXT[r.label] ?? r.label}
                </span>
                <span
                  className="min-w-0 flex-1 truncate text-sm text-ink-2"
                  title={r.name}
                >
                  {r.name || r.info_hash}
                </span>
                <span className="shrink-0 text-xs text-mute">
                  {ACTION_TEXT[r.action] ?? r.action}
                </span>
                <span className="shrink-0 text-xs tabular-nums text-faint">
                  {formatRelativeTime(r.created_at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
