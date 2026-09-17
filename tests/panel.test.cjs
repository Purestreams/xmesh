const { test } = require("node:test");
const assert = require("node:assert/strict");
const {
  sampleRates,
  selectionPlan,
  formatBytes,
  defaultRealityTarget,
} = require("../internal/controller/panel.js");

test("REALITY defaults follow the explicitly selected Gateway region", () => {
  assert.equal(defaultRealityTarget("cn"), "api.bilibili.com:443");
  assert.equal(defaultRealityTarget("overseas"), "www.swift.com:443");
  assert.equal(defaultRealityTarget(""), "");
});
const sample = (minute, bytes, generation = 1, ready = true) => ({
  at: new Date(Date.UTC(2026, 8, 17, 0, minute)).toISOString(),
  upload_bytes: bytes,
  download_bytes: bytes * 2,
  generation,
  ready,
});
test("rates exclude reset, generation changes, gaps and offline intervals", () => {
  const rates = sampleRates([
    sample(0, 100),
    sample(5, 700),
    sample(10, 10),
    sample(15, 610, 2),
    sample(30, 1000, 2),
    sample(35, 1600, 2, false),
    sample(40, 2000, 2),
  ]);
  assert.deepEqual(
    rates.map((s) => s.uploadRate),
    [null, 2, null, null, null, null, null],
  );
  assert.equal(rates[1].downloadRate, 4);
});
test("selection preview handles existing disabled grants and duplicate selections", () => {
  const data = {
    grants: [
      { user_id: "u", route_id: "a", enabled: true },
      { user_id: "u", route_id: "b", enabled: false },
    ],
    routes: [],
    links: [],
  };
  assert.deepEqual(
    selectionPlan("subscribe", "u", ["a", "b", "c", "c"], data),
    { created: 1, reenabled: 1, unchanged: 1 },
  );
  assert.deepEqual(selectionPlan("subscribe", "u", [], data), {
    created: 0,
    reenabled: 0,
    unchanged: 0,
  });
  assert.equal(data.grants.length, 2); // Clearing is not revocation.
});
test("assignment preview requires both an enabled route and enabled Link", () => {
  const data = {
    routes: [
      { id: "r1", agent_id: "a", gateway_id: "g1", enabled: true },
      { id: "r2", agent_id: "a", gateway_id: "g2", enabled: true },
    ],
    links: [
      { route_id: "r1", enabled: true },
      { route_id: "r2", enabled: false },
    ],
    grants: [],
  };
  assert.deepEqual(selectionPlan("assign", "a", ["g1", "g2", "g3"], data), {
    created: 1,
    reenabled: 1,
    unchanged: 1,
  });
});
test("traffic uses explicit binary units and missing values stay unknown", () => {
  assert.equal(formatBytes(1024), "1.0 KiB");
  assert.equal(formatBytes(null), "—");
  assert.equal(formatBytes(0), "0 B");
});
