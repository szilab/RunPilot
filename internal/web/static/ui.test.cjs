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
    assert.equal(await page.locator("#accountName").textContent(), "admin");
    await page.keyboard.press("Control+k");
    await page.locator("#workspaceSearch").fill("terminal");
    await page.keyboard.press("Enter");
    await page.locator("#terminalPage.active").waitFor();
    await page.locator(".xterm-helper-textarea").waitFor();
    await page.locator(".xterm-helper-textarea").focus();
    await page.keyboard.press("Control+k");
    assert.equal(await page.locator("#workspaceSearch").getAttribute("aria-expanded"), "false", "workspace shortcut intercepted terminal input");
    await page.locator('[data-page="overview"]').click();
    await page.locator(".host-dashboard-storage > summary").click();
    await page.evaluate(() => hostDashboard.refresh());
    assert.equal(await page.locator(".host-dashboard-storage").evaluate(details => details.open), true, "refresh closed filesystem details");
    assert.equal(await page.evaluate(() => document.activeElement.dataset.dashboardControl), "filesystems", "refresh lost dashboard keyboard focus");
    await page.locator(".host-dashboard-storage > summary").click();
    await page.locator('[data-page="settings"]').click();
    assert.equal(await page.evaluate(() => document.activeElement.id), "pageTitle", "navigation should focus the page introduction");
    assert.equal(await page.locator("#settingsPage #passwordChangeForm").count(), 0, "account form still lives in Settings");
    assert.equal(await page.locator("#settingsPage").getByText("Administrator account", {exact:true}).count(), 0);
    await page.locator("#generalSettingsTab").focus();
    await page.keyboard.press("ArrowRight");
    assert.equal(await page.locator("#pluginSettingsTab").getAttribute("aria-selected"), "true");
    await page.locator('[data-plugin-id="terminal"] .plugin-config-panel > summary').click();
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
    const screenshot = async (label, fullPage = true) => {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), label + " overflows");
      if (process.env.RUNPILOT_UI_SCREENSHOTS) {
        await fs.mkdir(process.env.RUNPILOT_UI_SCREENSHOTS, {recursive:true});
        await page.screenshot({path:path.join(process.env.RUNPILOT_UI_SCREENSHOTS, `${base ? "nested" : "root"}-${label}.png`),fullPage});
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
          if (id === "settings") await page.locator("#generalSettingsTab").click();
          if (id === "terminal") await page.locator(".xterm-helper-textarea").waitFor();
          await page.evaluate(() => scrollTo(0,0));
          await screenshot(`${id}-${theme}-${width}`);
        }
        await page.locator(".account-menu > summary").click();
        await page.locator("#accountSettingsButton").click();
        await page.locator("#accountSettingsDialog[open]").waitFor();
        assert.equal(await page.locator("#terminalPage").evaluate(el => el.classList.contains("active")), true, "account popup navigated away from the current page");
        assert.equal(await page.locator(".account-menu").evaluate(el => el.open), false);
        assert.equal(await page.locator("#accountSettingsName").textContent(), "admin");
        assert.equal(await page.evaluate(() => document.activeElement.id), "currentPassword");
        const dialog = await page.locator("#accountSettingsDialog").evaluate(el => ({width:el.clientWidth,scroll:el.scrollWidth}));
        assert.ok(dialog.scroll <= dialog.width + 1, "account popup overflow");
        await screenshot(`account-${theme}-${width}`, false);
        await page.locator("#currentPassword").fill("unsaved password draft");
        if (theme === "light") await page.keyboard.press("Escape");
        else await page.locator('#accountSettingsDialog button[data-dismiss]').filter({hasText:"Cancel"}).click();
        await page.locator("#accountSettingsDialog").waitFor({state:"hidden"});
        await page.waitForFunction(() => document.activeElement.matches(".account-menu > summary"));
        assert.equal(await page.locator("#currentPassword").inputValue(), "", "closing the popup retained a password draft");
      }
    }
    await page.locator(".account-menu > summary").click();
    await page.locator("#accountSettingsButton").click();
    await page.locator("#currentPassword").fill("browser review password");
    await page.locator("#nextPassword").fill("changed browser review password");
    await page.locator("#confirmPassword").fill("mismatched browser review password");
    await page.locator('#passwordChangeForm button[type="submit"]').click();
    await page.locator("#passwordChangeError:not(.hidden)").waitFor();
    assert.match(await page.locator("#passwordChangeError").textContent(), /confirmation does not match/);
    await page.locator('[aria-label="Close account settings"]').click();
    await page.locator("#accountSettingsDialog").waitFor({state:"hidden"});
    await page.locator(".account-menu > summary").click();
    await page.locator("#accountSettingsButton").click();
    assert.equal(await page.locator("#passwordChangeError").getAttribute("class"), "form-error hidden");
    await page.locator("#currentPassword").fill("browser review password");
    await page.locator("#nextPassword").fill("changed browser review password");
    await page.locator("#confirmPassword").fill("changed browser review password");
    await page.locator('#passwordChangeForm button[type="submit"]').click();
    await page.locator("#loginDialog[open]").waitFor();
    await page.locator("#loginUsername").fill("admin");
    await page.locator("#loginPassword").fill("changed browser review password");
    await page.locator("#loginForm button").click();
    await page.locator("#loginDialog").waitFor({state:"hidden"});
    await page.locator(".account-menu > summary").click();
    await page.locator("#logoutButton").click();
    await page.locator("#loginDialog[open]").waitFor();
    assert.equal(await page.evaluate(() => localStorage.getItem("runpilot.token")), null);
    assert.deepEqual(errors, []);
    await context.close();
    console.log("Login, keyboard tabs, preserved drafts, plugin search, confirmations, themes, responsive shell, Terminal, account popup/password change, nested basePath, and logout passed");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
