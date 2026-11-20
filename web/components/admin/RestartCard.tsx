"use client";

// Service-restart card (settings → 服务与数据库, above the form).
//
// Settings marked "restart required" (listen address, database,
// fetcher backend, sampler counts…) only take effect once the Go
// service restarts. This card lets an admin trigger that restart
// without an SSH session, through the same intent-file channel as
// system updates: the button only writes a restart-request flag via
// the Go backend; the root-side installer agent picks it up within
// a minute, restarts dhtsearch.service — never the web service, so
// this page itself stays up — and records the outcome, which this
// card polls through /api/admin-data.
//
// The poll doubles as the recovery check: while the restart runs,
// the API is briefly unreachable and every poll fails. That is the
// expected middle of the flow, so the card shows 重启中… rather
// than an error, and only declares 已重启 once the API answers
// again with the agent's "succeeded" record for this request.

import { useEffect, useRef, useState } from "react";
import type { AdminRestartState } from "@/lib/admin";

const POLL_ACTIVE_MS = 3_000;
const POLL_IDLE_MS = 30_000;
// A restart normally resolves in well under two minutes (the agent
// ticks every minute; the restart itself takes seconds). Past this
// the service may not be coming back on its own.
const SLOW_AFTER_MS = 4 * 60_000;

type Phase =
  | "idle"
  | "submitting"
  | "waiting" // request written; the agent has not picked it up yet
  | "restarting" // agent is on it, or the API has stopped answering
  | "recovered" // our request succeeded and the API answers again
  | "failed"; // our request finished as failed

type PollResult =
  | { ok: true; state: AdminRestartState }
  | { ok: false };

async function fetchRestart(): Promise<PollResult> {
  try {
    const res = await fetch(`/api/admin-data?kind=restart`, {
      cache: "no-store",
    });
    if (!res.ok) return { ok: false };
    return { ok: true, state: (await res.json()) as AdminRestartState };
  } catch {
    return { ok: false };
  }
}

async function postRestart(): Promise<{
  status: number;
  body: AdminRestartState & { error?: string };
}> {
  const res = await fetch("/api/admin-action", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind: "restart-service" }),
  });
  const body = (await res.json().catch(() => ({}))) as AdminRestartState & {
    error?: string;
  };
  return { status: res.status, body };
}

export default function RestartCard({
  initial,
}: {
  initial: AdminRestartState | null;
}) {
  const [data, setData] = useState<AdminRestartState | null>(initial);
  const [phase, setPhase] = useState<Phase>(() =>
    initial?.pending
      ? initial.state === "running"
        ? "restarting"
        : "waiting"
      : "idle",
  );
  const [confirming, setConfirming] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [slow, setSlow] = useState(false);

  // The request this card is tracking (ours, or one already in
  // flight when the page loaded), and when tracking started.
  const trackIdRef = useRef<string>(
    initial?.pending ? initial.request_id : "",
  );
  const trackSinceRef = useRef<number>(0);
  useEffect(() => {
    // A restart already in flight at page load counts as tracked
    // from now (its true start is the agent's business; the slow
    // warning just needs a lower bound).
    if (initial?.pending && trackSinceRef.current === 0) {
      trackSinceRef.current = Date.now();
    }
    // Mount-only: `initial` is the server-rendered snapshot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const phaseRef = useRef(phase);
  useEffect(() => {
    phaseRef.current = phase;
  }, [phase]);

  // Two-tap confirm disarms itself after a few seconds.
  useEffect(() => {
    if (!confirming) return;
    const t = window.setTimeout(() => setConfirming(false), 5_000);
    return () => window.clearTimeout(t);
  }, [confirming]);

  useEffect(() => {
    let timer = 0;
    let stopped = false;
    const tick = async () => {
      if (!document.hidden) {
        const res = await fetchRestart();
        if (!stopped) {
          const cur = phaseRef.current;
          const tracking =
            cur === "waiting" || cur === "restarting" || cur === "submitting";
          if (res.ok) {
            setData(res.state);
            if (tracking && trackIdRef.current) {
              if (res.state.request_id === trackIdRef.current) {
                if (res.state.state === "succeeded") setPhase("recovered");
                else if (res.state.state === "failed") setPhase("failed");
                else if (res.state.state === "running")
                  setPhase("restarting");
                else if (res.state.state === "requested")
                  setPhase("waiting");
              } else if (!res.state.pending) {
                // The record moved on without our id (a newer
                // request superseded it): stop tracking.
                trackIdRef.current = "";
                setPhase("idle");
              }
            }
          } else if (tracking) {
            // The API stopped answering mid-flow: that IS the
            // restart happening, not an error.
            setPhase("restarting");
          }
          if (
            tracking &&
            trackSinceRef.current > 0 &&
            Date.now() - trackSinceRef.current > SLOW_AFTER_MS
          ) {
            setSlow(true);
          }
        }
      }
      if (!stopped) {
        const active =
          phaseRef.current === "waiting" ||
          phaseRef.current === "restarting" ||
          phaseRef.current === "submitting";
        timer = window.setTimeout(tick, active ? POLL_ACTIVE_MS : POLL_IDLE_MS);
      }
    };
    timer = window.setTimeout(tick, POLL_IDLE_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  async function onRestart() {
    if (phase === "submitting" || phase === "waiting" || phase === "restarting")
      return;
    if (!confirming) {
      setConfirming(true);
      return;
    }
    setConfirming(false);
    setPhase("submitting");
    setNotice(null);
    setSlow(false);
    try {
      const { status, body } = await postRestart();
      if (status === 202) {
        trackIdRef.current = body.request_id;
        trackSinceRef.current = Date.now();
        setData(body);
        setPhase("waiting");
      } else if (status === 409) {
        setNotice(body.error ?? "已有重启请求在等待执行。");
        setPhase("idle");
        const res = await fetchRestart();
        if (res.ok) setData(res.state);
      } else {
        setNotice(`提交重启请求失败：${body.error ?? `HTTP ${status}`}`);
        setPhase("idle");
      }
    } catch {
      setNotice("提交重启请求失败：无法连接到后端。");
      setPhase("idle");
    }
  }

  if (!data) {
    return (
      <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
        <h2 className="text-base font-semibold">重启服务</h2>
        <p className="mt-1 text-sm text-mute">
          暂时读不到重启状态（后端不可达或版本过旧），稍后刷新页面重试。
        </p>
      </section>
    );
  }

  const inFlight =
    phase === "submitting" || phase === "waiting" || phase === "restarting";
  const showStatus =
    inFlight ||
    phase === "recovered" ||
    phase === "failed" ||
    data.state === "succeeded" ||
    data.state === "failed";

  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">重启服务</h2>
        <div className="ml-auto flex items-center gap-2">
          {confirming && !inFlight && (
            <button
              type="button"
              onClick={() => setConfirming(false)}
              className="inline-flex min-h-11 items-center rounded-lg px-3 text-sm text-mute transition-colors hover:text-ink"
            >
              取消
            </button>
          )}
          <button
            type="button"
            onClick={onRestart}
            disabled={inFlight || !data.contract_ready}
            title={
              data.contract_ready
                ? "把重启请求写入数据目录，由 root 侧脚本在一分钟内执行"
                : "当前部署的数据目录契约不可用"
            }
            className={
              confirming
                ? "inline-flex min-h-11 items-center rounded-lg border border-danger bg-danger/15 px-4 text-sm font-semibold text-danger-ink transition-colors disabled:cursor-not-allowed disabled:opacity-50"
                : "inline-flex min-h-11 items-center rounded-lg border border-danger/60 px-4 text-sm font-medium text-danger-ink transition-colors hover:bg-danger/10 disabled:cursor-not-allowed disabled:opacity-50"
            }
          >
            {phase === "submitting"
              ? "提交中…"
              : phase === "waiting"
                ? "等待执行…"
                : phase === "restarting"
                  ? "重启中…"
                  : confirming
                    ? "确认重启？再次点击立即执行"
                    : "重启服务"}
          </button>
        </div>
      </div>

      <p className="mt-2 text-sm leading-relaxed text-mute">
        标注「重启后生效」的设置（监听地址、数据库、抓取底座、采样器数量等）保存后需要重启
        API 服务才生效，在这里可以直接发起，不用再开 SSH。重启只针对 API／爬虫服务：抓取会暂停几秒，管理接口短暂不可达；Web
        界面本身不重启，本卡会自动确认服务恢复。
      </p>

      {showStatus && (
        <div className="mt-3 rounded-lg border border-line-2 bg-soft px-3 py-2 text-sm leading-relaxed">
          {phase === "waiting" && (
            <p>
              <span className="font-medium">重启请求已提交，等待脚本执行</span>
              <span className="ml-2 text-mute">
                脚本侧的代理会在约 1 分钟内执行，执行时本卡会短暂失去与后端联系，属正常现象。
              </span>
            </p>
          )}
          {phase === "restarting" && (
            <p>
              <span className="inline-flex items-center gap-2 font-medium">
                <span className="h-2 w-2 animate-pulse rounded-full bg-danger" />
                重启中…
              </span>
              <span className="ml-2 text-mute">
                服务正在重启，管理接口暂时不可达，本卡正在等待它恢复。
              </span>
            </p>
          )}
          {phase === "recovered" && (
            <p>
              <span className="font-medium">已重启，服务已恢复</span>
              {data.finished_at && (
                <span className="ml-2 text-mute">完成于 {data.finished_at}</span>
              )}
              {data.message && (
                <span className="ml-2 text-mute">{data.message}</span>
              )}
            </p>
          )}
          {(phase === "failed" || (phase === "idle" && data.state === "failed")) && (
            <p>
              <span className="font-medium text-danger-ink">上次重启失败</span>
              {data.finished_at && (
                <span className="ml-2 text-mute">{data.finished_at}</span>
              )}
              {data.message && (
                <p className="mt-1 break-words text-mute">{data.message}</p>
              )}
            </p>
          )}
          {phase === "idle" && data.state === "succeeded" && (
            <p>
              <span className="font-medium">上次重启成功</span>
              {data.finished_at && (
                <span className="ml-2 text-mute">完成于 {data.finished_at}</span>
              )}
              {data.requested_by && (
                <span className="ml-2 text-mute">（由 {data.requested_by} 发起）</span>
              )}
            </p>
          )}
          {slow && inFlight && (
            <p className="mt-2 text-warn-ink">
              重启耗时异常久，服务可能未能自行恢复。请通过 SSH 检查：
              journalctl -u dhtsearch -n 100
            </p>
          )}
        </div>
      )}

      {notice && <p className="mt-3 text-sm text-warn-ink">{notice}</p>}
      {!data.contract_ready && (
        <p className="mt-3 text-sm text-warn-ink">
          当前部署的数据目录契约不可用：面板无法与脚本通信，面板重启不可用（多见于未用
          install.sh 部署的环境），请通过 SSH 重启服务。
        </p>
      )}
    </section>
  );
}
