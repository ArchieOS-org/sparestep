// Optional developer check for the focused-task report UI. The product itself
// does not need Node or Playwright. Run this after rebuilding the Sparestep
// binary so its embedded assets contain the focus UI.
import { spawn } from 'node:child_process';
import assert from 'node:assert/strict';

const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright');
const binary = process.env.SPARESTEP_BINARY || './dist/sparestep-linux-amd64';
const server = spawn(binary, ['serve', '--demo', '--port', '0'], { stdio: ['ignore', 'pipe', 'pipe'] });
let browser;

const focusReport = {
  id: 'focus-demo-1',
  goal: 'Keep the report focused while making the setup reliable',
  status: 'active',
  mode: 'planning',
  hook_observed: true,
  criteria: [
    {
      id: 'criteria-1',
      description: 'The focused setup path is ready to implement',
      kind: 'outcome',
      status: 'ready',
      evidence: ['The required setup path is recorded.']
    },
    {
      id: 'criteria-2',
      description: 'The necessary follow-up remains visible after reload',
      kind: 'coverage',
      status: 'done',
      evidence: ['The scope amendment is durable in the report.']
    }
  ],
  paths: ['/home/noah/sparestep/internal/web/assets/app.js'],
  amendments: [
    {
      criterion_id: 'criteria-1',
      reason: 'The report needs a persistent status view before implementation can start.',
      evidence: ['The current task has no visible focus state.'],
      paths: ['/home/noah/sparestep/internal/web/assets/index.html'],
      created_at: '2026-09-08T01:02:03Z'
    }
  ],
  checks: [
    {
      id: 'check-1',
      criterion_id: 'criteria-1',
      name: 'Browser report check',
      kind: 'check',
      status: 'pass',
      evidence: 'The report renders without browser errors.'
    }
  ],
  coverage_gaps: ['Backend wiring still needs an integration check.']
};

const waitingFocusReport = { ...focusReport, hook_observed: false };
const completedFocusReport = { ...focusReport, status: 'completed' };

const deferred = [
  {
    id: 'deferred-queued',
    task_id: 'focus-demo-1',
    title: 'Document the setup follow-up',
    body: 'This remains local until the Linear connection is ready.',
    status: 'queued',
    issue_url: '',
    last_error: ''
  },
  {
    id: 'deferred-created',
    task_id: 'focus-demo-1',
    title: 'Track the confirmed setup follow-up',
    body: 'The issue was confirmed by Linear.',
    status: 'created',
    issue_url: 'https://linear.app/dispatch/issue/DIS-123/track-setup',
    last_error: ''
  },
  {
    id: 'deferred-invalid-url',
    task_id: 'focus-demo-1',
    title: 'Ignore an untrusted issue link',
    body: 'Only confirmed Linear links should become actionable links.',
    status: 'created',
    issue_url: 'https://example.invalid/not-linear',
    last_error: ''
  }
];

async function waitForAddress() {
  return new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => reject(new Error('Report server did not start')), 10000);
    server.stdout.on('data', (chunk) => {
      output += chunk;
      const match = output.match(/Browser address: (http:\/\/127\.0\.0\.1:\d+\/#token=[a-f0-9]+)/);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
    server.once('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`Server exited: ${code}`));
    });
  });
}

function mergeReport(base, state) {
  return { ...base, focus: state.focus, deferred: state.deferred };
}

try {
  const address = await waitForAddress();
  browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || '/usr/bin/google-chrome',
    headless: true,
    args: ['--no-sandbox']
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 960 } });
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    // The local report intentionally probes /api/report once before posting
    // the one-time fragment token, so Chromium logs that expected 401.
    if (message.type() === 'error' && !message.text().includes('401 (Unauthorized)')) errors.push(message.text());
  });

  let state = { focus: null, deferred: [] };
  await page.route('**/api/report', async (route) => {
    const response = await route.fetch();
    if (!response.ok()) {
      await route.fulfill({ response });
      return;
    }
    const base = await response.json();
    const headers = response.headers();
    delete headers['content-length'];
    await route.fulfill({
      status: response.status(),
      headers,
      body: JSON.stringify(mergeReport(base, state))
    });
  });

  await page.goto(address);
  await page.locator('#focus-content').waitFor();
  await page.getByText('No focused task', { exact: true }).waitFor();
  await page.getByText('/sparestep <what you want done>', { exact: true }).waitFor();
  assert.equal(await page.locator('#scope-section').isVisible(), false, 'scope notice is hidden without focus');
  assert.equal(await page.locator('#deferred-section').isVisible(), false, 'deferred section is hidden when empty');

  state = { focus: waitingFocusReport, deferred };
  await page.reload();
  await page.getByText(focusReport.goal, { exact: true }).waitFor();
  await page.getByText('Waiting for native guards', { exact: true }).waitFor();

  state = { focus: focusReport, deferred };
  await page.reload();
  await page.getByText('Native guards observed', { exact: true }).waitFor();
  await page.locator('.focus-card .meta').filter({ hasText: 'Planning' }).waitFor();
  await page.getByText('Success criteria', { exact: true }).waitFor();
  await page.getByText('The focused setup path is ready to implement', { exact: true }).waitFor();
  await page.getByText('Ready', { exact: true }).waitFor();
  await page.getByText('The required setup path is recorded.', { exact: true }).waitFor();
  await page.getByText('Browser report check', { exact: true }).waitFor();
  const focusText = await page.locator('#focus-content').textContent();
  assert.ok(!focusText.includes('[object Object]'), 'focus objects must not render as object strings');
  assert.ok(!focusText.includes(JSON.stringify(focusReport.checks[0])), 'focus checks must not render raw JSON');
  await page.getByText('Backend wiring still needs an integration check.', { exact: true }).waitFor();

  const scope = page.locator('#scope-section');
  await scope.waitFor({ state: 'visible' });
  await page.getByText(focusReport.amendments[0].reason, { exact: true }).waitFor();
  await page.getByText(focusReport.amendments[0].paths[0], { exact: true }).waitFor();

  state = { focus: completedFocusReport, deferred };
  await page.reload();
  await page.locator('#focus-status').getByText('Done', { exact: true }).waitFor();
  await page.getByText(focusReport.goal, { exact: true }).waitFor();

  state = { focus: focusReport, deferred };
  await page.reload();
  await page.locator('#scope-section').waitFor({ state: 'visible' });
  await page.getByText(focusReport.amendments[0].reason, { exact: true }).waitFor();

  await page.getByText('Document the setup follow-up', { exact: true }).waitFor();
  await page.getByText('Queued — not filed yet', { exact: true }).waitFor();
  await page.getByRole('link', { name: 'Open confirmed Linear issue' }).waitFor();
  assert.equal(await page.getByRole('link', { name: 'Open confirmed Linear issue' }).count(), 1, 'only the confirmed Linear URL is linked');
  const untrustedCard = page.locator('.deferred-card').filter({ hasText: 'Ignore an untrusted issue link' });
  assert.equal(await untrustedCard.getByRole('link').count(), 0, 'untrusted URL is not linked');

  const details = page.locator('details.details-panel');
  const activitySummary = details.locator(':scope > summary');
  assert.equal(await details.evaluate((node) => node.open), false, 'Activity reports start collapsed');
  await activitySummary.click();
  await page.locator('#findings .finding').first().waitFor({ state: 'visible' });
  assert.equal(await page.locator('#findings .finding').count() > 0, true, 'legacy findings remain behind Activity reports');
  await activitySummary.click();
  assert.equal(await details.evaluate((node) => node.open), false, 'Activity reports can be collapsed');

  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile layout must not overflow');
  assert.deepEqual(errors, [], 'browser runtime errors');
  console.log('PASS: empty/planning focus, persistent scope amendment, criteria/evidence, deferred queue/link safety, legacy activity details, and mobile layout.');
} finally {
  if (browser) await browser.close();
  server.kill('SIGTERM');
}
