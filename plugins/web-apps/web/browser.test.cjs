// Optional real-browser integration: RUNPILOT_PLAYWRIGHT_MODULE points to playwright-core.
const assert = require("node:assert/strict");
const { chromium } = require(process.env.RUNPILOT_PLAYWRIGHT_MODULE);
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.RUNPILOT_BROWSER_EXECUTABLE });
  try {
    const context = await browser.newContext();
    const shell = await context.newPage();
    await shell.addInitScript(({ token, path }) => { if (location.pathname === path) localStorage.setItem("runpilot.token", token); }, { token: process.env.RUNPILOT_TEST_TOKEN, path: process.env.RUNPILOT_TEST_BASE + "/" });
    await shell.goto(process.env.RUNPILOT_TEST_URL + process.env.RUNPILOT_TEST_BASE + "/");
    await shell.waitForFunction(() => document.querySelector('[data-page="web-apps"]'));
    assert.equal(await shell.evaluate(() => localStorage.getItem("runpilot.token")), null);
    assert.equal(await shell.evaluate(() => sessionStorage.getItem("runpilot.token")), process.env.RUNPILOT_TEST_TOKEN);
    await shell.click('[data-page="web-apps"]');
    await shell.getByRole("button", { name: "Open", exact: true }).waitFor();
    const appPromise = context.waitForEvent("page");
    await shell.getByRole("button", { name: "Open", exact: true }).click();
    const app = await appPromise;
    app.on("pageerror", error => console.error("application page error:", error.message));
    await app.waitForFunction(() => document.querySelector("#app-ready"), { timeout: 15000 });
    assert.ok(app.url().endsWith(process.env.RUNPILOT_TEST_BASE + "/app/"));
    assert.equal(await app.evaluate(() => window.opener), null);
    assert.equal(await app.evaluate(() => sessionStorage.getItem("runpilot.token")), null);
    assert.equal(await app.evaluate(() => localStorage.getItem("runpilot.token")), null);
    assert.equal(await app.evaluate(() => document.querySelector("link").sheet.cssRules[0].selectorText), "#app-ready");
    assert.equal(await app.evaluate(() => document.querySelector("img").naturalWidth), 1);
    const result = await app.evaluate(async () => {
      await fetch("login", { method: "POST", body: "login-data" });
      const cookie = await (await fetch("cookie")).text();
      const ranged = await fetch("range", { headers: { Range: "bytes=2-5" } });
      const range = { status: ranged.status, contentRange: ranged.headers.get("Content-Range"), body: await ranged.text() };
      const compressed = await (await fetch("gzip")).text();
      const redirect = await fetch("redirect");
      const post = await (await fetch("echo", { method: "POST", body: "x".repeat(400000) })).text();
      let large = 0; const reader = (await fetch("large")).body.getReader(); for (;;) { const chunk = await reader.read(); if (chunk.done) break; large += chunk.value.length; }
      const registration = await navigator.serviceWorker.getRegistration();
      return { cookie, range, compressed, redirect: await redirect.text(), post: post.length, large, scope: registration.scope, browserCookies: document.cookie };
    });
    assert.equal(result.cookie, "private-cookie"); assert.equal(result.browserCookies, "");
    assert.deepEqual(result.range, { status: 206, contentRange: "bytes 2-5/10", body: "2345" });
    assert.equal(result.compressed, "compressed response"); assert.equal(result.redirect, "private-cookie");
    assert.equal(result.post, 400000); assert.equal(result.large, 20 * 1024 * 1024);
    assert.equal(new URL(result.scope).pathname, process.env.RUNPILOT_TEST_BASE + "/app/");
    // Reuse the installed narrow worker in a second noopener tab, with an isolated cookie jar.
    await shell.evaluate(() => localStorage.setItem("runpilot.token", "legacy shared credential"));
    const secondPromise = context.waitForEvent("page"); await shell.getByRole("button", { name: "Open", exact: true }).click(); const second = await secondPromise;
    await second.waitForFunction(() => document.querySelector("#app-ready"));
    assert.equal(await second.evaluate(async () => (await fetch("cookie")).text()), "");
    assert.equal(await second.evaluate(() => localStorage.getItem("runpilot.token")), null);
    assert.equal(await second.evaluate(() => sessionStorage.getItem("runpilot.token")), null);
    await second.close();
    await shell.getByRole("button", { name: /Close sessions/ }).click();
    assert.equal(await app.evaluate(async () => (await fetch("cookie")).status), 502);
    async function addTarget(name, mount) {
      await shell.getByRole("button", { name: "Add Web App" }).click();
      const dialog = shell.locator("dialog.webapps-dialog");
      await dialog.getByLabel("Name", { exact: true }).fill(name);
      await dialog.getByLabel("Mount path", { exact: true }).fill(mount);
      await dialog.getByLabel("Upstream HTTP(S) origin", { exact: true }).fill(process.env.RUNPILOT_TEST_UPSTREAM);
      await dialog.getByLabel("Upstream base path", { exact: true }).fill(process.env.RUNPILOT_TEST_UPSTREAM_BASE);
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      await dialog.waitFor({ state: "detached" });
    }
    await addTarget("CRUD target", "/crud");
    let card = shell.locator(".webapps-card").filter({ hasText: "CRUD target" });
    await card.getByRole("button", { name: "Edit", exact: true }).click();
    await shell.locator("dialog.webapps-dialog").getByLabel("Name", { exact: true }).fill("CRUD renamed");
    await shell.locator("dialog.webapps-dialog").getByLabel("Mount path", { exact: true }).fill("/crud/moved");
    await shell.locator("dialog.webapps-dialog").getByRole("button", { name: "Save", exact: true }).click();
    await shell.locator("dialog.webapps-dialog").waitFor({ state: "detached" });
    card = shell.locator(".webapps-card").filter({ hasText: "CRUD renamed" });
    shell.once("dialog", dialog => dialog.accept()); await card.getByRole("button", { name: "Delete", exact: true }).click();
    await card.waitFor({ state: "detached" });
    const original = shell.locator(".webapps-card").filter({ hasText: "Test app" });
    shell.once("dialog", dialog => dialog.accept()); await original.getByRole("button", { name: "Delete", exact: true }).click();
    await original.waitFor({ state: "detached" });
    await addTarget("Replacement", "/app");
    const replacementPromise = context.waitForEvent("page"); await shell.getByRole("button", { name: "Open", exact: true }).click();
    const replacement = await replacementPromise; await replacement.waitForFunction(() => document.querySelector("#app-ready"));
    assert.ok([502, 503].includes(await app.evaluate(async () => (await fetch("cookie")).status)), "stale client gained replacement session access");
    console.log("browser gateway, CRUD, stale publication isolation, credential isolation, relative assets, cookies, redirects, Range, streaming and close passed");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
