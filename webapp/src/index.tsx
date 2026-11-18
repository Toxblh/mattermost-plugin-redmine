import React, { useEffect, useState } from "react";
import { Card, read, write, clear, fresh } from "./cache";
import { parseLink, formatLinks } from "./format";
const ID = "space.altlinux.redmine";
type Connection = {
  connected: boolean;
  revision: string;
  display_name: string;
  server_url: string;
};
let store: any;
let conn: Connection | undefined;
let generation = 0;
let currentUser = "";
let broadcast: BroadcastChannel | undefined;
let active = true;
let registryInstance: any;
let formatID: string | undefined;
let formatTimer: ReturnType<typeof setTimeout> | undefined;
let unsub: (() => void) | undefined;
let clientOffset = 0;
const pending = new Map<string, Promise<Card>>();
const shown = new Map<string, Card>();
const listeners = new Set<() => void>();
const notify = () => {
  listeners.forEach((f) => f());
  refreshFormat();
};
const user = () => store?.getState()?.entities?.users?.currentUserId || "";
const base = () => `${(window as any).basename || ""}/plugins/${ID}/api/v1`;
async function request(path: string, options: RequestInit = {}) {
  const csrf = document.cookie
    .split("; ")
    .find((v) => v.startsWith("MMCSRF="))
    ?.slice(7);
  const res = await fetch(base() + path, {
    credentials: "same-origin",
    cache: "no-store",
    ...options,
    headers: {
      "Content-Type": "application/json",
      ...(csrf ? { "X-CSRF-Token": decodeURIComponent(csrf) } : {}),
      ...options.headers,
    },
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || "upstream_unavailable");
  return data;
}
async function reset(announce = true) {
  generation++;
  conn = undefined;
  shown.clear();
  pending.clear();
  await clear();
  if (announce) broadcast?.postMessage({ reset: true });
  notify();
}
async function connection() {
  const g = generation;
  const info = (await request("/connection")) as Connection;
  if (!active || g !== generation) throw new Error("connection_changed");
  if (
    conn &&
    (conn.revision !== info.revision || conn.server_url !== info.server_url)
  ) {
    await reset();
  }
  conn = info;
  notify();
  return info;
}
async function getCard(id: string, refresh = false): Promise<Card> {
  const g = generation;
  const c = conn || (await connection());
  if (!c.connected) throw new Error("connect_required");
  const key = JSON.stringify([
    location.origin,
    user(),
    c.server_url,
    c.revision,
    id,
  ]);
  if (!refresh) {
    const saved = await read(key);
    if (g !== generation) throw new Error("connection_changed");
    if (saved) {
      shown.set(id, saved);
      setTimeout(() => {
        if (g === generation && active && shown.get(id) === saved) {
          shown.delete(id);
          notify();
        }
      }, Math.max(0, saved.expires_at - Date.now()));
      notify();
      return saved;
    }
  }
  if (pending.has(key)) return pending.get(key)!;
  const call = request(`/issues/${id}${refresh ? "?refresh=1" : ""}`)
    .then(async (raw: Card) => {
      if (g !== generation || !active || raw.revision !== conn?.revision)
        throw new Error("connection_changed");
      const now = Date.now();
      /* server elapsed age survives different client clocks */ const serverNow =
        Number((raw as any).server_now || now);
      clientOffset = now - serverNow;
      const card = {
        ...raw,
        source_fetched_at: raw.source_fetched_at + clientOffset,
        expires_at: raw.expires_at + clientOffset,
      };
      await write(key, card, () => g === generation && active);
      if (g !== generation) throw new Error("connection_changed");
      shown.set(id, card);
      setTimeout(
        () => {
          if (g === generation && active && shown.get(id) === card) {
            shown.delete(id);
            notify();
          }
        },
        Math.max(0, card.expires_at - Date.now()),
      );
      notify();
      return card;
    })
    .finally(() => pending.delete(key));
  pending.set(key, call);
  return call;
}
const errors: Record<string, string> = {
  connect_required: "Подключите личный аккаунт Redmine",
  invalid_api_key: "Ключ недействителен или отозван",
  unavailable: "Задача недоступна",
  server_not_configured: "Администратору нужно указать URL Redmine",
  encryption_not_configured: "Администратору нужно настроить шифрование ключей",
  connection_changed: "Подключение изменилось. Откройте карточку снова.",
  busy: "Много запросов. Повторите позже.",
};
const message = (e: unknown) =>
  errors[(e as Error)?.message] ||
  "Не удалось связаться с Redmine. Повторите запрос.";
function useTick() {
  const [, tick] = useState(0);
  useEffect(() => {
    const cb = () => tick((n) => n + 1);
    listeners.add(cb);
    return () => {
      listeners.delete(cb);
    };
  }, []);
}
function openSettings() {
  window.dispatchEvent(new Event("redmine-open-settings"));
}
function Tooltip({ href, show }: { href: string; show: boolean }) {
  useTick();
  const id = conn && parseLink(href, conn.server_url);
  const [data, setData] = useState<Card>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [clock, tick] = useState(Date.now());
  useEffect(() => {
    if (!show || !id) return;
    let alive = true;
    const t = setTimeout(() => {
      setLoading(true);
      getCard(id)
        .then((c) => {
          if (alive) setData(c);
        })
        .catch((e) => {
          if (alive) setError(message(e));
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    }, 300);
    const interval = setInterval(() => tick(Date.now()), 1000);
    return () => {
      alive = false;
      clearTimeout(t);
      clearInterval(interval);
    };
  }, [id, show]);
  if (!id || !show) return null;
  const c =
    data && data.revision === conn?.revision && fresh(data, clock)
      ? data
      : undefined;
  return (
    <div className="rm-card" role="dialog" aria-label={`Redmine ${id}`}>
      <header>
        <strong>▣ Redmine · #{id}</strong>
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          aria-label="Открыть задачу"
        >
          ↗
        </a>
      </header>
      {c ? (
        <>
          <h3>{c.title}</h3>
          <div className="rm-badges">
            <span>{c.status}</span>
            <span>{c.priority}</span>
          </div>
          <dl>
            <dt>Исполнитель</dt>
            <dd>{c.assignee || "Не назначен"}</dd>
            <dt>Проект</dt>
            <dd>{c.project}</dd>
            <dt>Тип</dt>
            <dd>{c.tracker}</dd>
          </dl>
          <p className="rm-description">{c.description}</p>
          <small>
            Обновлено в трекере: {new Date(c.updated).toLocaleString()}
          </small>
          <footer>
            Данные получены{" "}
            {Math.max(0, Math.floor((clock - c.source_fetched_at) / 60000))} мин
            назад
            <button
              onClick={() => {
                setLoading(true);
                setError("");
                getCard(id, true)
                  .then(setData)
                  .catch((e) => {
                    setData(undefined);
                    setError(message(e));
                  })
                  .finally(() => setLoading(false));
              }}
              disabled={loading}
            >
              ⟳
            </button>
          </footer>
        </>
      ) : (
        <p>
          {loading
            ? "Загрузка…"
            : error || "Срок кэша истёк. Откройте карточку снова."}
        </p>
      )}
      {error && c && <p role="alert">{error}</p>}
      {!conn?.connected && (
        <button onClick={openSettings}>Подключить Redmine</button>
      )}
    </div>
  );
}
function Settings() {
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useTick();
  useEffect(() => {
    const f = () => setOpen(true);
    window.addEventListener("redmine-open-settings", f);
    return () => window.removeEventListener("redmine-open-settings", f);
  }, []);
  if (!open) return null;
  return (
    <div className="rm-overlay" onClick={() => setOpen(false)}>
      <section
        className="rm-settings"
        role="dialog"
        aria-modal="true"
        aria-label="Подключение Redmine"
        onClick={(e) => e.stopPropagation()}
      >
        <header>
          <h2>Личный аккаунт Redmine</h2>
          <button onClick={() => setOpen(false)} aria-label="Закрыть">
            ×
          </button>
        </header>
        <p>{conn?.server_url || "URL сервера задаётся администратором"}</p>
        <p>
          {conn?.connected
            ? `Подключён: ${conn.display_name}`
            : "Аккаунт не подключён"}
        </p>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError("");
            try {
              await request("/connection", {
                method: "POST",
                body: JSON.stringify({ api_key: key }),
              });
              setKey("");
              await reset();
              await connection();
            } catch (e) {
              setError(message(e));
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>
            Личный API-ключ
            <input
              type="password"
              autoComplete="off"
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
          </label>
          <p>
            <small>
              Ключ передаётся серверу MM по HTTPS и хранится зашифрованным. Его
              можно получить в «Моей учётной записи» Redmine.
            </small>
          </p>
          {error && <p role="alert">{error}</p>}
          <button type="submit" disabled={busy || !key}>
            Подключить / заменить ключ
          </button>
          {conn?.connected && (
            <button
              type="button"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await request("/connection", { method: "DELETE" });
                  await reset();
                  await connection();
                } catch (e) {
                  setError(message(e));
                } finally {
                  setBusy(false);
                }
              }}
            >
              Отключить
            </button>
          )}
        </form>
      </section>
    </div>
  );
}
function refreshFormat() {
  if (!active || formatTimer || !registryInstance) return;
  formatTimer = setTimeout(() => {
    formatTimer = undefined;
    if (!active) return;
    if (formatID) registryInstance.unregisterComponent(formatID);
    formatID = registryInstance.registerMessageWillFormatHook(
      (_post: unknown, message: string) =>
        conn
          ? formatLinks(message, conn.server_url, (id) => {
              const c = shown.get(id);
              return c && fresh(c) ? c : undefined;
            })
          : message,
    );
  }, 80);
}
const style = `.rm-card{width:360px;max-width:85vw;padding:16px;color:var(--center-channel-color);background:var(--center-channel-bg);border:1px solid rgba(var(--center-channel-color-rgb),.2);border-radius:8px;box-shadow:0 8px 24px #0003;font-size:13px;white-space:normal}.rm-card header,.rm-card footer,.rm-settings header{display:flex;justify-content:space-between;gap:12px;align-items:center}.rm-card h3{font-size:16px;line-height:1.4;margin:14px 0}.rm-badges{display:flex;gap:8px}.rm-badges span{background:rgba(var(--center-channel-color-rgb),.08);padding:3px 7px;border-radius:4px}.rm-card dl{display:grid;grid-template-columns:110px 1fr;gap:6px;margin:14px 0}.rm-card dd{margin:0;overflow-wrap:anywhere}.rm-card dt,.rm-card small{opacity:.7}.rm-description{white-space:pre-wrap;overflow-wrap:anywhere;max-height:180px;overflow:auto}.rm-card footer{margin-top:12px;font-size:11px;opacity:.8}.rm-card button,.rm-settings button{border:1px solid rgba(var(--center-channel-color-rgb),.2);border-radius:4px;padding:6px 10px;background:var(--center-channel-bg);color:var(--center-channel-color);cursor:pointer}.rm-overlay{position:fixed;inset:0;background:#0006;z-index:9999;display:flex;align-items:center;justify-content:center}.rm-settings{width:480px;max-width:95vw;padding:24px;border-radius:8px;background:var(--center-channel-bg);color:var(--center-channel-color)}.rm-settings input{display:block;width:100%;padding:8px;margin-top:8px;background:var(--center-channel-bg);color:var(--center-channel-color);border:1px solid #888;border-radius:4px}.rm-settings button{margin-right:8px}`;
class Plugin {
  initialize(registry: any, s: any) {
    store = s;
    registryInstance = registry;
    active = true;
    currentUser = user();
    const el = document.createElement("style");
    el.id = "rm-plugin-style";
    el.textContent = style;
    document.head.appendChild(el);
    broadcast =
      typeof BroadcastChannel !== "undefined"
        ? new BroadcastChannel(ID)
        : undefined;
    if (broadcast)
      broadcast.onmessage = () => {
        void reset(false).then(() => {
          if (user()) void connection().catch(() => {});
        });
      };
    registry.registerGlobalComponent(Settings);
    registry.registerMainMenuAction("Подключить Redmine", openSettings);
    registry.registerLinkTooltipComponent(Tooltip);
    registry.registerWebSocketEventHandler(
      `custom_${ID}_connection_changed`,
      () => {
        void reset(false)
          .then(() => connection())
          .catch(() => {});
      },
    );
    registry.registerWebSocketEventHandler(`custom_${ID}_configuration_changed`, () => {
      void reset(false).then(() => connection()).catch(() => {});
    });
    unsub = store.subscribe(() => {
      const next = user();
      if (next !== currentUser) {
        currentUser = next;
        void reset().then(() => {
          if (next) void connection().catch(() => {});
        });
      }
    });
    refreshFormat();
    if (currentUser) void connection().catch(() => {});
  }
  uninitialize() {
    active = false;
    generation++;
    unsub?.();
    if (formatTimer) clearTimeout(formatTimer);
    if (formatID) registryInstance.unregisterComponent(formatID);
    broadcast?.close();
    shown.clear();
    pending.clear();
    document.getElementById("rm-plugin-style")?.remove();
  }
}
(window as any).registerPlugin(ID, new Plugin());
