// Optional developer check. The product itself does not need Node or Playwright.
import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const binary = process.env.SPARESTEP_BINARY || './dist/sparestep-linux-amd64';
const server = spawn(binary, ['serve', '--demo', '--port', '0'], { stdio: ['ignore', 'pipe', 'pipe'] });
let browser;

async function openActivity(page) {
  const details = page.locator('details.details-panel');
  if (!(await details.evaluate((node) => node.open))) await details.locator(':scope > summary').click();
  await page.locator('#findings .finding').first().waitFor({ state: 'visible' });
}

try {
  const address = await new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => reject(new Error('Report server did not start')), 10000);
    server.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/Browser address: (http:\/\/127\.0\.0\.1:\d+\/#token=[a-f0-9]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    server.once('exit', code => { clearTimeout(timer); reject(new Error(`Server exited: ${code}`)); });
  });
  browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 960 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(address);
  await page.locator('#demo-banner').waitFor({ state: 'visible' });
  await openActivity(page);
  await page.getByRole('button', { name: 'Review issue draft' }).first().waitFor();
  assert.equal(new URL(page.url()).hash, '', 'access fragment should be cleared');
  if (process.env.SPARESTEP_SCREENSHOT) {
    await page.screenshot({ path: process.env.SPARESTEP_SCREENSHOT, fullPage: true });
  }
  await page.getByRole('button', { name: 'This was necessary', exact: true }).first().click();
  await page.locator('#history .finding').waitFor();
  assert.equal(await page.locator('#findings .finding').count(), 0);
  await page.getByRole('button', { name: 'Undo', exact: true }).click();
  await page.locator('#findings .finding').waitFor();
  await page.reload();
  await openActivity(page);
  await page.getByRole('button', { name: 'Dismiss', exact: true }).first().click();
  await page.locator('#history .finding').waitFor();
  await page.getByRole('button', { name: 'Undo', exact: true }).click();
  await page.locator('#findings .finding').waitFor();
  await page.getByRole('button', { name: 'Review issue draft' }).first().click();
  await page.getByLabel('Issue title', { exact: true }).waitFor();
  await page.getByLabel('Issue title', { exact: true }).fill('Fix setup <example>');
  await page.getByLabel('Issue description', { exact: true }).fill('Reviewed draft with evidence & acceptance criteria.');
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download Markdown' }).click();
  const download = await downloadPromise;
  const markdown = await readFile(await download.path(), 'utf8');
  assert.match(markdown, /Fix setup <example>/);
  assert.match(markdown, /Reviewed draft with evidence & acceptance criteria\./);
  // Capture the handoff URL without sending a request to Linear.
  await page.evaluate(() => { window.open = url => { window.__linearURL = url; return {}; }; });
  await page.getByRole('button', { name: 'Open in Linear', exact: true }).click();
  const linearURL = new URL(await page.evaluate(() => window.__linearURL));
  assert.equal(linearURL.origin, 'https://linear.new');
  assert.equal(linearURL.searchParams.get('title'), 'Fix setup <example>');
  assert.equal(linearURL.searchParams.get('description'), 'Reviewed draft with evidence & acceptance criteria.');
  await page.getByLabel('Existing Linear issue link').fill('https://linear.app/test/issue/TEST-1/example');
  await page.getByRole('button', { name: 'Save link', exact: true }).click();
  await page.waitForFunction(() => document.getElementById('issue-status').textContent.includes('Linked'));
  await page.getByRole('button', { name: 'Close issue draft' }).click();
  await page.reload();
  await openActivity(page);
  await page.getByRole('link', { name: 'Open linked Linear issue' }).waitFor();
  await page.getByRole('button', { name: 'Pause Sparestep', exact: true }).click();
  await page.getByRole('button', { name: 'Resume Sparestep', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Resume Sparestep', exact: true }).click();
  await page.getByRole('button', { name: 'Pause Sparestep', exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile layout must not overflow');
  assert.deepEqual(errors, [], 'browser runtime errors');
  console.log('PASS: auth/reload, findings, necessary/dismiss/undo, edited draft download/handoff, saved issue link, pause/resume, and mobile layout.');
} finally {
  if (browser) await browser.close();
  server.kill('SIGTERM');
}
