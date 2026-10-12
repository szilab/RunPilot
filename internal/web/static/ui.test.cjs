// Optional browser regression test, launched by TestUIRealBrowser.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const {chromium} = require(process.env.RUNPILOT_PLAYWRIGHT_MODULE);
(async () => {
  const browser = await chromium.launch({headless:true, executablePath:process.env.RUNPILOT_BROWSER_EXECUTABLE});
  try {
    const context = await browser.newContext({viewport:{width:1440,height:1000}, reducedMotion:"reduce"});
    const page = await context.newPage(), errors = [];
    page.on("pageerror", error => errors.push(error.message));
    const base = process.env.RUNPILOT_TEST_BASE.replace(/\/$/, "");
    await page.goto(process.env.RUNPILOT_TEST_URL + base + "/");
    await page.locator("#loginDialog[open]").waitFor();
    await page.keyboard.press("Escape");
    assert.equal(await page.locator("#loginDialog").evaluate(dialog => dialog.open), true);
    await page.locator("#loginUsername").fill("admin");
    await page.locator("#loginPassword").fill("browser review password");
    await page.locator("#loginForm button").click();
    await page.locator(".host-dashboard-name").waitFor();
    await page.locator('[data-page="terminal"]').waitFor();
    await page.evaluate(() => document.fonts.ready);
    assert.equal(await page.evaluate(() => document.fonts.check("14px Inter")), true);
    await page.locator('[data-page="settings"]').click();
    assert.equal(await page.evaluate(() => document.activeElement.id), "pageTitle", "navigation should focus the page introduction");
    await page.locator("#generalSettingsTab").focus();
    await page.keyboard.press("ArrowRight");
    assert.equal(await page.locator("#pluginSettingsTab").getAttribute("aria-selected"), "true");
    await page.locator('[data-plugin-id="terminal"] [name="command"]').waitFor({state:"visible"});
    await page.waitForFunction(() => !document.querySelector('[data-plugin-id="terminal"] [name="command"]').disabled);
    await page.locator('[data-plugin-id="terminal"] [name="command"]').fill("/bin/sh");
    await page.waitForTimeout(5500);
    assert.equal(await page.locator('[data-plugin-id="terminal"] [name="command"]').inputValue(), "/bin/sh", "polling erased a settings draft");
    await page.locator("#pluginSearch").fill("missing-plugin");
    assert.equal(await page.locator(".plugin-setting-card:visible").count(), 0);
    await page.locator("#pluginSearch").fill("");
    await page.locator('[data-plugin-id="terminal"] .button.danger').click();
    await page.locator(".confirmation-dialog[open]").waitFor();
    assert.equal(await page.evaluate(() => document.activeElement.textContent), "Cancel");
    await page.keyboard.press("Escape");
    await page.locator(".confirmation-dialog").waitFor({state:"detached"});
    assert.equal(await page.locator('[data-plugin-id="terminal"]').count(), 1);
    const screenshot = async label => {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), label + " overflows");
      if (process.env.RUNPILOT_UI_SCREENSHOTS) {
        await fs.mkdir(process.env.RUNPILOT_UI_SCREENSHOTS, {recursive:true});
        await page.screenshot({path:path.join(process.env.RUNPILOT_UI_SCREENSHOTS, `${base ? "nested" : "root"}-${label}.png`),fullPage:true});
      }
    };
    for (const width of [1440,820,390]) {
      await page.setViewportSize({width,height:width===390?844:1000});
      for (const theme of ["light","dark"]) {
        const current = await page.locator("html").getAttribute("data-theme");
        if (theme !== current) await page.locator("#themeToggle").click();
        assert.equal(await page.locator("html").getAttribute("data-theme"), theme);
        for (const id of ["overview","settings","terminal"]) {
          await page.locator(`[data-page="${id}"]`).click();
          assert.equal(await page.locator(`[data-page="${id}"]`).getAttribute("aria-current"), "page");
          if (id === "terminal") await page.locator(".xterm-helper-textarea").waitFor();
          await page.evaluate(() => scrollTo(0,0));
          await screenshot(`${id}-${theme}-${width}`);
        }
      }
    }
    await page.locator("#logoutButton").click();
    await page.locator("#loginDialog[open]").waitFor();
    assert.equal(await page.evaluate(() => localStorage.getItem("runpilot.token")), null);
    assert.deepEqual(errors, []);
    await context.close();
    console.log("Login, keyboard tabs, preserved drafts, plugin search, safe confirmations, theme switching, responsive shell, Terminal, nested basePath, and logout passed");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
