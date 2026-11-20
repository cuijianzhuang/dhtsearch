"use client";

// System-update status card (settings page, above the form).
//
// The panel never upgrades anything itself: "检查更新" asks the Go
// backend (which caches GitHub's answer for an hour), and "立即升级"
// only writes an upgrade-request flag into the data dir. The
// root-side installer agent picks the flag up within a minute, runs
// the normal upgrade path and records the outcome, which this card
// polls through /api/admin-data and displays. Polling is brisk while
// a request is in flight and lazy otherwise, and pauses on hidden
// tabs like the overview dashboard's does.

import { useEffect, useRef, useState } from "react";
import type { AdminUpdate } from "@/lib/admin";

const POLL_ACTIVE_MS = 5_000;
const POLL_IDLE_MS = 30_000;

async function fetchUpdate(): Promise<AdminUpdate> {
  const res = await fetch(`/api/admin-data?kind=update`, {
    cache: "no-store",
  });
  if (!res.ok) throw new Error(`admin-data update: ${res.status}`);
  return (await res.json()) as AdminUpdate;
}

async function postAction(kind: string): Promise<{ status: number; body: AdminUpdate & { error?: string } }> {
  const res = await fetch("/api/admin-action", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind }),
  });
  const body = (await res.json().catch(() => ({}))) as AdminUpdate & {
    error?: string;
  };
  return { status: res.status, body };
}

function fmtUnix(sec: number): string {
  if (!sec) return "从未";
  return new Date(sec * 1000).toLocaleString("zh-CN", { hour12: false });
}

function fmtISO(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString("zh-CN", { hour12: false });
}

const STATE_LABELS: Record<string, string> = {
  requested: "已提交，等待脚本执行",
  running: "升级执行中",
  succeeded: "升级成功",
  failed: "升级失败",
};

export default function UpdateCard({ initial }: { initial: AdminUpdate | null }) {
  const [data, setData] = useState<AdminUpdate | null>(initial);
  const [busy, setBusy] = useState<"" | "check" | "request">("");
  const [notice, setNotice] = useState<string | null>(null);
  const dataRef = useRef(data);
  useEffect(() => {
    dataRef.current = data;
  }, [data]);

  const inFlight =
    data?.upgrade.pending === true ||
    data?.upgrade.state === "requested" ||
    data?.upgrade.state === "running";

  useEffect(() => {
    let timer = 0;
    let stopped = false;
    const tick = async () => {
      if (!document.hidden) {
        try {
          const fresh = await fetchUpdate();
          if (!stopped) setData(fresh);
        } catch {
          // Keep the last good data; the next tick retries.
        }
      }
      if (!stopped) {
        const active =
          dataRef.current?.upgrade.pending === true ||
          dataRef.current?.upgrade.state === "requested" ||
          dataRef.current?.upgrade.state === "running";
        timer = window.setTimeout(tick, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
      }
    };
    timer = window.setTimeout(tick, POLL_IDLE_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  async function onCheck() {
    if (busy) return;
    setBusy("check");
    setNotice(null);
    try {
      const { status, body } = await postAction("check-update");
      if (status === 200) {
        setData(body);
      } else {
        setNotice(`检查更新失败：${body.error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("检查更新失败：无法连接到后端。");
    } finally {
      setBusy("");
    }
  }

  async function onRequest() {
    if (busy || inFlight) return;
    setBusy("request");
    setNotice(null);
    try {
      const { status, body } = await postAction("request-upgrade");
      if (status === 202) {
        setData(body);
        setNotice("升级请求已提交：脚本侧的升级代理会在约 1 分钟内开始执行，本卡会自动刷新进度。");
      } else if (status === 409) {
        setNotice(body.error ?? "已有升级正在进行。");
        // Refresh so the card shows the in-flight run it just learned about.
        try {
          setData(await fetchUpdate());
        } catch {
          /* keep current data */
        }
      } else {
        setNotice(`提交升级请求失败：${body.error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("提交升级请求失败：无法连接到后端。");
    } finally {
      setBusy("");
    }
  }

  if (!data) {
    return (
      <section className="mb-4 rounded-xl border border-line bg-surface p-4 shadow-card">
        <h2 className="text-base font-semibold">系统更新</h2>
        <p className="mt-1 text-sm text-mute">
          暂时读不到更新状态（后端不可达或版本过旧），稍后刷新页面重试。
        </p>
      </section>
    );
  }

  const au = data.auto_update;
  const up = data.upgrade;
  const showUpgrade = up.state !== "idle" || up.pending;

  return (
    <section className="mb-4 rounded-xl border border-line bg-surface p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">系统更新</h2>
        {data.update_available ? (
          <span className="rounded-md bg-accent-soft px-2 py-1 text-xs font-medium text-accent-ink">
            有新版本可升级
          </span>
        ) : data.latest_version ? (
          <span className="rounded-md bg-soft px-2 py-1 text-xs font-medium text-mute">
            已是最新版本
          </span>
        ) : null}
        <div className="ml-auto flex gap-2">
          <button
            type="button"
            onClick={onCheck}
            disabled={busy !== ""}
            className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy === "check" ? "检查中…" : "检查更新"}
          </button>
          <button
            type="button"
            onClick={onRequest}
            disabled={busy !== "" || inFlight || !data.contract_ready}
            title={
              data.contract_ready
                ? "把升级请求写入数据目录，由 root 侧脚本在一分钟内执行"
                : "当前部署的数据目录契约不可用"
            }
            className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy === "request" ? "提交中…" : inFlight ? "升级进行中…" : "立即升级"}
          </button>
        </div>
      </div>

      <dl className="mt-3 grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div className="flex gap-2">
          <dt className="shrink-0 text-faint">当前版本</dt>
          <dd className="font-medium tabular-nums">{data.current_version}</dd>
        </div>
        <div className="flex gap-2">
          <dt className="shrink-0 text-faint">最新版本</dt>
          <dd className="font-medium tabular-nums">
            {data.latest_version || "尚未检查"}
            {data.latest_published_at && (
              <span className="ml-2 font-normal text-mute">
                发布于 {fmtISO(data.latest_published_at)}
              </span>
            )}
          </dd>
        </div>
        <div className="flex gap-2">
          <dt className="shrink-0 text-faint">上次检查</dt>
          <dd>
            {fmtUnix(data.checked_at)}
            {data.check_error && (
              <span className="ml-2 text-warn-ink">
                最近一次检查失败（{data.check_error}），显示的是上次结果
              </span>
            )}
          </dd>
        </div>
        <div className="flex gap-2">
          <dt className="shrink-0 text-faint">自动升级</dt>
          <dd>
            {au.enabled
              ? `已开启 · ${au.freq === "weekly" ? "每周" : "每天"} ${au.time}`
              : "已关闭"}
            <span className="ml-2 text-mute">
              {au.source === "panel"
                ? "（以面板设置为准）"
                : au.source === "script"
                  ? "（以脚本 CLI 状态为准，在下方保存开关/时间后由面板接管）"
                  : "（未配置，在下方开启）"}
            </span>
          </dd>
        </div>
        {au.last_result && (
          <div className="flex gap-2 sm:col-span-2">
            <dt className="shrink-0 text-faint">上次自动检查</dt>
            <dd>
              {au.last_check}
              <span className="ml-2 text-mute">{au.last_result}</span>
            </dd>
          </div>
        )}
      </dl>

      {showUpgrade && (
        <div className="mt-3 rounded-lg border border-line-2 bg-soft px-3 py-2 text-sm leading-relaxed">
          <span className="font-medium">
            {STATE_LABELS[up.state] ?? up.state}
          </span>
          {(up.from_version || up.to_version) && (
            <span className="ml-2 tabular-nums text-mute">
              {up.from_version}
              {up.to_version ? ` → ${up.to_version}` : ""}
            </span>
          )}
          <span className="ml-2 text-mute">
            {up.requested_at && `请求于 ${up.requested_at}`}
            {up.requested_by && `（${up.requested_by}）`}
            {up.finished_at && ` · 完成于 ${up.finished_at}`}
          </span>
          {up.message && <p className="mt-1 break-words text-mute">{up.message}</p>}
        </div>
      )}

      {notice && <p className="mt-3 text-sm text-warn-ink">{notice}</p>}
      {!data.contract_ready && (
        <p className="mt-3 text-sm text-warn-ink">
          当前部署的数据目录契约不可用：面板无法与升级脚本通信，「立即升级」与自动升级设置同步不可用（多见于未用 install.sh 部署的环境）。
        </p>
      )}
    </section>
  );
}
