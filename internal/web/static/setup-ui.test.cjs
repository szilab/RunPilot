// Optional setup/expiry appearance checks, launched by TestSetupAppearanceRealBrowser.
const assert=require("node:assert/strict"),fs=require("node:fs/promises"),path=require("node:path");
const {chromium}=require(process.env.RUNPILOT_PLAYWRIGHT_MODULE);
(async()=>{
  const browser=await chromium.launch({headless:true,executablePath:process.env.RUNPILOT_BROWSER_EXECUTABLE});
  try {
    for(const width of [1440,390,320])for(const theme of ["light","dark"]){
      const context=await browser.newContext({viewport:{width,height:width===1440?1000:844},colorScheme:theme});
      const page=await context.newPage(),errors=[];page.on("pageerror",error=>errors.push(error.message));
      const response=await page.goto(process.env.RUNPILOT_TEST_URL);
      await page.evaluate(()=>document.fonts.ready);
      assert.equal(await page.locator("html").getAttribute("data-theme"),theme);
      const locked=process.env.RUNPILOT_SETUP_STATE==="expired";
      assert.equal(response.status(),locked?410:200);
      if(locked){assert.equal(await page.locator("#setup").count(),0);assert.match(await page.locator("h1").textContent(),/expired/);}
      else {
        await page.locator('[name="password"]').fill("browser appearance password");
        await page.locator('[name="confirm"]').fill("different appearance password");
        await page.locator("#setup button").click();
        await page.locator("#error:not(.hidden)").waitFor();
        assert.match(await page.locator("#error").textContent(),/do not match/);
      }
      assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),"setup page overflow");
      const card=await page.locator(".setup-card").evaluate(el=>({width:el.clientWidth,scroll:el.scrollWidth,left:el.scrollLeft}));
      assert.ok(card.scroll<=card.width+1&&card.left===0,"setup card overflow "+JSON.stringify(card));
      if(process.env.RUNPILOT_UI_SCREENSHOTS){await fs.mkdir(process.env.RUNPILOT_UI_SCREENSHOTS,{recursive:true});await page.screenshot({path:path.join(process.env.RUNPILOT_UI_SCREENSHOTS,`${process.env.RUNPILOT_SETUP_STATE}-${theme}-${width}.png`),fullPage:true});}
      assert.deepEqual(errors,[]);await context.close();
    }
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
