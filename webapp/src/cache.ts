export type Card = {
  id: number;
  title: string;
  description: string;
  status: string;
  priority: string;
  assignee: string;
  project: string;
  tracker: string;
  updated: string;
  url: string;
  source_fetched_at: number;
  expires_at: number;
  revision: string;
};
export const fresh = (c: Card, now = Date.now()) =>
  c.source_fetched_at <= now &&
  now < c.expires_at &&
  c.expires_at - c.source_fetched_at <= 300000;
const name = "space.altlinux.redmine.cards.v1";
const memory = new Map<string, Card>();
let database: Promise<IDBDatabase> | undefined;
function db(): Promise<IDBDatabase> {
  if (!database)
    database = new Promise((resolve, reject) => {
      const r = indexedDB.open(name, 1);
      r.onupgradeneeded = () => r.result.createObjectStore("cards");
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    });
  return database;
}
export async function read(key: string): Promise<Card | undefined> {
  let c = memory.get(key);
  if (!c) {
    try {
      const d = await db();
      c = await new Promise<Card | undefined>((resolve, reject) => {
        const r = d.transaction("cards").objectStore("cards").get(key);
        r.onsuccess = () => resolve(r.result);
        r.onerror = () => reject(r.error);
      });
    } catch {
      /* memory fallback */
    }
  }
  if (c && fresh(c)) {
    memory.set(key, c);
    return c;
  }
  memory.delete(key);
  return undefined;
}
export async function write(
  key: string,
  c: Card,
  valid: () => boolean = () => true,
) {
  if (!valid() || !fresh(c)) return;
  memory.set(key, c);
  if (memory.size > 500) memory.delete(memory.keys().next().value!);
  try {
    const d = await db();
    if (!valid()) return;
    await new Promise<void>((resolve, reject) => {
      const tx = d.transaction("cards", "readwrite");
      tx.objectStore("cards").put(c, key);
      const r = tx.objectStore("cards").openCursor();
      let count = 0;
      r.onsuccess = () => {
        const cursor = r.result;
        if (!cursor) return;
        if (!fresh(cursor.value) || ++count > 500) cursor.delete();
        cursor.continue();
      };
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
  } catch {
    /* memory fallback */
  }
}
export async function clear() {
  memory.clear();
  try {
    const d = await db();
    await new Promise<void>((resolve, reject) => {
      const tx = d.transaction("cards", "readwrite");
      tx.objectStore("cards").clear();
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
  } catch {
    /* optional storage */
  }
}
