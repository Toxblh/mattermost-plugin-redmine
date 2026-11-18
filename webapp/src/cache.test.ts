import { test } from "node:test";
import assert from "node:assert/strict";
import "fake-indexeddb/auto";
import { Card, clear, fresh, read, write } from "./cache";
const make = (revision = "a"): Card => ({
  id: 42,
  title: "Private",
  description: "",
  status: "Open",
  priority: "",
  assignee: "",
  project: "",
  tracker: "",
  updated: "",
  url: "https://redmine.test/issues/42",
  source_fetched_at: Date.now(),
  expires_at: Date.now() + 300000,
  revision,
});
test("expires at original source time, rejects backwards clock and excess TTL", () => {
  const c = make();
  assert.equal(fresh(c, c.source_fetched_at + 299999), true);
  assert.equal(fresh(c, c.source_fetched_at + 300000), false);
  assert.equal(fresh(c, c.source_fetched_at - 1), false);
  assert.equal(
    fresh({ ...c, expires_at: c.source_fetched_at + 400000 }),
    false,
  );
});
test("IndexedDB keys isolate accounts and logout fences late writes", async () => {
  await clear();
  await write("alice/rev1/42", make());
  assert.equal((await read("alice/rev1/42"))?.title, "Private");
  assert.equal(await read("bob/rev1/42"), undefined);
  assert.equal(await read("alice/rev2/42"), undefined);
  await clear();
  await write("alice/rev1/42", make(), () => false);
  assert.equal(await read("alice/rev1/42"), undefined);
});
import { formatLinks, parseLink } from "./format";
test("strict instance matching and safe local message formatting", () => {
  const base = "https://redmine.test/tracker";
  const href = base + "/issues/42";
  assert.equal(parseLink(href + "#note-1", base), "42");
  assert.equal(
    parseLink("https://redmine.test.evil/tracker/issues/42", base),
    undefined,
  );
  assert.equal(
    parseLink("https://redmine.test/tracker-other/issues/42", base),
    undefined,
  );
  assert.equal(
    parseLink("https://redmine.test/tracker/issues/0", base),
    undefined,
  );
  assert.equal(
    formatLinks("`" + href + "`", base, () => undefined),
    "`" + href + "`",
  );
  assert.equal(
    formatLinks("[custom](" + href + ")", base, () => undefined),
    "[custom](" + href + ")",
  );
  const formatted = formatLinks(href, base, () => ({
    ...make(),
    title: "[private] *title*",
  }));
  assert.ok(formatted.includes("\\[private\\] \\*title\\*"));
  assert.ok(formatted.endsWith("](" + href + ")"));
});
