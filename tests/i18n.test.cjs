const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { chooseLanguage, normalizeLanguage } = require("../internal/controller/i18n.js");
const source = fs.readFileSync(path.join(__dirname, "../internal/controller/i18n.js"), "utf8");
function translations(language) {
  const context = { module: { exports: {} }, navigator: { languages: [language] } };
  vm.runInNewContext(source, context);
  return context.module.exports;
}

test("first visit follows the browser's primary language, with English fallback", () => {
  for (const language of ["zh-CN", "zh-TW", "zh-HK", "ZH-hans", "zh_Hant"]) {
    assert.equal(chooseLanguage(null, [language, "en-US"]), "zh");
  }
  assert.equal(chooseLanguage(null, ["en-GB", "zh-CN"]), "en");
  assert.equal(chooseLanguage(null, ["fr-FR", "zh-CN"]), "en");
  assert.equal(chooseLanguage(null, []), "en");
  assert.equal(normalizeLanguage("zhuang"), null);
});

test("a valid explicit preference overrides browser language; invalid saved values are ignored", () => {
  assert.equal(chooseLanguage("en", ["zh-CN"]), "en");
  assert.equal(chooseLanguage("zh", ["en-US"]), "zh");
  assert.equal(chooseLanguage("invalid", ["zh-HK"]), "zh");
});

test("interpolation preserves user content, including placeholders and replacement tokens", () => {
  const english = translations("en-US"), chinese = translations("zh-CN");
  const name = "总览 {1} $& <script>";
  assert.equal(english.t("节点组合：{0}", name), "Node pair: " + name);
  assert.equal(chinese.t("节点组合：{0}", name), "节点组合：" + name);
  assert.equal(english.t("已发布 2 / 已授权 3"), "Published 2 / Authorized 3");
  assert.equal(chinese.t("Subscription routes: 2 created, 1 re-enabled, 0 already active"), "订阅线路：新增 2，重新启用 1，已开启 0");
  assert.equal(english.t("unknown runtime error"), "unknown runtime error");
  assert.equal(chinese.t("Username"), "用户名");
  assert.equal(english.t("action.disable"), "Disable");
  assert.equal(chinese.t("action.enable"), "启用");
});

test("static and generated interface messages have English translations", () => {
  const { t } = translations("en-US");
  const directory = path.join(__dirname, "../internal/controller");
  for (const file of ["templates.go", "upstream_panel.go", "monitor.go", "server.go", "monitor.html", "monitor_admin.html", "panel.js", "monitor.js"]) {
    const content = fs.readFileSync(path.join(directory, file), "utf8");
    const messages = [...content.matchAll(/ data-i18n>([^<{]+)<\/[^>]+>|<title data-i18n>([^<{]+)<\/title>|t\(("(?:[^"\\]|\\.)*")/g)];
    for (const match of messages) {
      const message = match[3] ? JSON.parse(match[3]) : match[1] || match[2];
      assert.ok(!/\p{Script=Han}/u.test(t(message)), `${file}: ${message}`);
    }
  }
});
