// Browser-driven smoke test for the expanded admin panel (Dashboard, Nodes,
// Accounts, API Keys, Admin Users, Audit Log) - not part of the app, not a
// permanent test suite, just real verification against the real backend per
// this project's "verify, don't just assert" rule.
import { chromium } from 'playwright';

const BASE_URL = process.env.E2E_BASE_URL ?? 'http://localhost:5173';
const USERNAME = process.env.E2E_USERNAME ?? 'admin';
const PASSWORD = process.env.E2E_PASSWORD ?? 'story65-test-password';
// Unique per run so the script is safely re-runnable against a stack that
// wasn't torn down between runs (name/subnet collisions otherwise 409).
const RUN_ID = process.env.E2E_RUN_ID ?? Math.random().toString(36).slice(2, 8);
const NODE_NAME = `e2e-node-${RUN_ID}`;
const ACCOUNT_LABEL = `e2e-account-${RUN_ID}`;
const KEY_LABEL = `e2e-bot-${RUN_ID}`;
const ADMIN_USERNAME = `e2e-support-${RUN_ID}`;
const SUBNET_OCTET = 100 + Math.floor(Math.random() * 100);
const NODE_SUBNET = `10.${SUBNET_OCTET}.0.0/24`;

function assert(condition, message) {
  if (!condition) {
    throw new Error(`ASSERTION FAILED: ${message}`);
  }
  console.log(`OK: ${message}`);
}

// Playwright's own bundled Chromium download is unreliable in this environment -
// drive the already-installed system Google Chrome instead via the 'chrome'
// channel, which needs no separate download.
const browser = await chromium.launch({ channel: 'chrome' });
try {
  const page = await browser.newPage();
  page.on('console', (msg) => {
    if (msg.type() === 'error') console.log(`[browser console error] ${msg.text()}`);
  });

  // 1. Unauthenticated visit to a protected route redirects to /login.
  await page.goto(`${BASE_URL}/dashboard`);
  await page.waitForURL(/\/login$/, { timeout: 5000 });
  assert(page.url().endsWith('/login'), 'unauthenticated /dashboard visit redirects to /login');

  // 2. Log in with real credentials against the real backend.
  await page.getByLabel('Username').fill(USERNAME);
  await page.getByLabel('Password').fill(PASSWORD);
  await page.getByRole('button', { name: /sign in/i }).click();
  await page.waitForURL(/\/dashboard$/, { timeout: 5000 });
  assert(page.url().endsWith('/dashboard'), 'login redirects to /dashboard');

  // 3. Sidebar shows all super_admin nav items and the signed-in username.
  for (const label of ['Dashboard', 'Nodes', 'Accounts', 'API Keys', 'Admin Users', 'Audit Log']) {
    await page.waitForSelector(`nav >> text=${label}`, { timeout: 5000 });
  }
  assert(true, 'sidebar shows all nav items for super_admin');
  await page.waitForSelector('text=admin');
  assert(true, 'sidebar shows signed-in username');

  // 4. Dashboard stat cards render (starts at zero nodes/accounts).
  await page.waitForSelector('text=Nodes online', { timeout: 5000 });
  assert(true, 'dashboard renders stat cards');

  // 5. Create a node.
  await page.getByRole('link', { name: 'Nodes' }).click();
  await page.waitForURL(/\/nodes$/);
  await page.getByRole('button', { name: /new node/i }).click();
  await page.getByPlaceholder('e.g. eu-west-1').fill(NODE_NAME);
  await page.getByPlaceholder('vpn1.example.com:51820').fill(`${NODE_NAME}.example.com:51820`);
  // Subnet is now a curated dropdown with a "Custom…" escape hatch; the random test
  // subnet isn't one of the presets, so pick Custom then type it into the revealed field.
  await page.locator('form select').selectOption('custom');
  await page.getByPlaceholder('10.66.0.0/24').fill(NODE_SUBNET);
  await page.getByRole('button', { name: /^create node$/i }).click();
  await page.waitForSelector(`td:has-text("${NODE_NAME}")`, { timeout: 5000 });
  assert(true, 'node created and appears in table');

  // 6. Generate a join token for it.
  await page.locator('tr', { hasText: NODE_NAME }).getByRole('button', { name: /join token/i }).click();
  await page.waitForSelector('text=/expires at/i', { timeout: 5000 });
  const tokenText = await page.locator('p.font-mono').first().innerText();
  assert(tokenText.length > 20, `join token dialog shows a real token (len=${tokenText.length})`);
  await page.locator('button[aria-label="Close"]').click();

  // 7. Create an account. The freshly UI-created node above is still "pending"
  // (no real WireGuard agent behind it yet, correctly rejected by the backend)
  // so pin this account to the pre-registered "agent-node" (a real cmd/agent
  // process was run against it before this script started) instead of the
  // default "all eligible nodes" fan-out - deterministic for this test, and
  // proves the pin-to-one-node override still works (docs/STORY-09).
  await page.getByRole('link', { name: 'Accounts' }).click();
  await page.waitForURL(/\/accounts$/);
  await page.getByRole('button', { name: /new account/i }).click();
  await page.getByPlaceholder('e.g. alice-laptop').fill(ACCOUNT_LABEL);
  await page.locator('select').first().selectOption({ label: 'Pin to just: agent-node (default)' });
  await page.getByRole('button', { name: /^create account$/i }).click();
  await page.waitForSelector(`td:has-text("${ACCOUNT_LABEL}")`, { timeout: 5000 });
  assert(true, 'account created and appears in table');

  // 8. Open the account, suspend it, re-enable it, then view its per-node config
  // (an account can have peers on several nodes now - see docs/STORY-09-multi-
  // node-accounts.md - so config is fetched per peer, not one global button).
  await page.locator('tr', { hasText: ACCOUNT_LABEL }).click();
  await page.waitForSelector('text=Suspend');
  await page.waitForSelector('text=Node peers (1)', { timeout: 5000 });
  assert(true, 'account detail shows exactly one node peer (pinned)');
  await page.getByRole('button', { name: /^suspend$/i }).click();
  await page.waitForSelector('span:has-text("suspended")', { timeout: 5000 });
  assert(true, 'account suspended via dialog action');
  await page.getByRole('button', { name: /^enable$/i }).click();
  await page.waitForSelector('span:has-text("active")', { timeout: 5000 });
  assert(true, 'account re-enabled via dialog action');
  await page.getByRole('button', { name: /^config$/i }).click();
  await page.waitForSelector('pre:has-text("[Interface]")', { timeout: 5000 });
  assert(true, 'wg-quick config rendered for the account\'s node peer');
  await page.locator('button[aria-label="Close"]').click();

  // 8b. Create a second account with the default "all eligible nodes" option
  // (no pin) and confirm it fans out to every currently-registered node - the
  // core behavior this story adds (docs/STORY-09-multi-node-accounts.md).
  const FANOUT_LABEL = `${ACCOUNT_LABEL}-fanout`;
  await page.getByRole('button', { name: /new account/i }).click();
  await page.getByPlaceholder('e.g. alice-laptop').fill(FANOUT_LABEL);
  await page.getByRole('button', { name: /^create account$/i }).click();
  await page.waitForSelector(`td:has-text("${FANOUT_LABEL}")`, { timeout: 5000 });
  await page.locator('tr', { hasText: FANOUT_LABEL }).click();
  await page.waitForSelector('text=/node peers \\(\\d+\\)/i', { timeout: 5000 });
  const peersHeading = await page.locator('text=/node peers \\(\\d+\\)/i').innerText();
  assert(/node peers \(([1-9]\d*)\)/i.test(peersHeading), `fan-out account got a peer on at least one registered node (${peersHeading})`);
  await page.locator('button[aria-label="Close"]').click();

  // 9. API Keys: create one, see the one-time secret, then rotate and revoke it.
  await page.getByRole('link', { name: 'API Keys' }).click();
  await page.waitForURL(/\/api-keys$/);
  await page.getByRole('button', { name: /new api key/i }).click();
  await page.getByPlaceholder('e.g. telegram-sales-bot').fill(KEY_LABEL);
  await page.getByRole('button', { name: /^create key$/i }).click();
  await page.waitForSelector('text=Secret key', { timeout: 5000 });
  const secretText = await page.locator('p.break-all').innerText();
  assert(secretText.length > 10, `one-time secret shown after creation (len=${secretText.length})`);
  await page.locator('button[aria-label="Close"]').click();
  await page.waitForSelector(`td:has-text("${KEY_LABEL}")`);
  await page.locator('tr', { hasText: KEY_LABEL }).getByRole('button', { name: /rotate/i }).click();
  await page.waitForSelector('text=Secret key', { timeout: 5000 });
  await page.locator('button[aria-label="Close"]').click();
  await page.locator('tr', { hasText: KEY_LABEL }).getByRole('button', { name: /revoke/i }).click();
  await page.getByRole('button', { name: /^revoke$/i }).last().click();
  await page.waitForSelector(`tr:has-text("${KEY_LABEL}") >> text=revoked`, { timeout: 5000 });
  assert(true, 'api key revoked');

  // 10. Admin Users: create a support-role admin.
  await page.getByRole('link', { name: 'Admin Users' }).click();
  await page.waitForURL(/\/admins$/);
  await page.getByRole('button', { name: /new admin/i }).click();
  await page.locator('input[autocomplete="off"]').fill(ADMIN_USERNAME);
  await page.locator('input[type="password"]').fill('e2e-support-password-123');
  await page.getByRole('button', { name: /^create admin$/i }).click();
  await page.waitForSelector(`td:has-text("${ADMIN_USERNAME}")`, { timeout: 5000 });
  assert(true, 'new admin user created and appears in table');

  // 11. Audit Log: entries exist for the actions above, and expanding one shows detail.
  await page.getByRole('link', { name: 'Audit Log' }).click();
  await page.waitForURL(/\/audit-log$/);
  await page.waitForSelector('table tbody tr', { timeout: 5000 });
  const rowCount = await page.locator('table tbody tr').count();
  assert(rowCount > 0, `audit log shows recorded actions (rows=${rowCount})`);
  await page.locator('table tbody tr').first().click();
  await page.waitForSelector('text=IP address', { timeout: 5000 });
  assert(true, 'audit log row expands to show detail');

  // 12. Session survives a reload via silent refresh.
  await page.reload();
  await page.waitForSelector('table', { timeout: 5000 });
  assert(page.url().includes('/audit-log'), 'session survives a reload via silent refresh');

  // 13. Logging out returns to /login and a subsequent protected visit redirects again.
  await page.locator('button', { hasText: 'Log out' }).click();
  await page.waitForURL(/\/login$/, { timeout: 5000 });
  await page.goto(`${BASE_URL}/accounts`);
  await page.waitForURL(/\/login$/, { timeout: 5000 });
  assert(page.url().endsWith('/login'), 'logout clears the session (protected route redirects again)');

  console.log('\nALL CHECKS PASSED');
} finally {
  await browser.close();
}
