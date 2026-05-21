// End-to-end happy-path for the livetv plugin.
//
// This file holds two specs:
//
//   * "smoke" - reliably exercises the e2e harness wiring: starts the
//     Go binary, hits /healthz, loads the SPA shell. This is the minimum
//     ship-gate.
//
//   * "happy-path" - admin adds an M3U source, refreshes, then an XMLTV
//     source, then a user tunes a channel. Marked `.skip` by default
//     because (a) the SPA's admin form selectors are tightly coupled to
//     Radix Dialog internals that need ARIA roles aligning across browsers
//     and (b) the player negotiation for the stub HLS stream needs MSE
//     wiring that this harness does not simulate. Flip to `.only` (or
//     remove the skip) once those pieces are sorted; the fixture server,
//     the e2e Go binary, the auth-bypass middleware, and the database
//     setup are all already in place.

import { test, expect } from '@playwright/test';

test.describe('livetv', () => {
  test('smoke: server up, healthz responds, SPA index loads', async ({ page, request }) => {
    const baseURL = process.env.E2E_BASE_URL;
    expect(baseURL, 'globalSetup must publish E2E_BASE_URL').toBeTruthy();

    const health = await request.get('/healthz');
    expect(health.status()).toBe(204);

    await page.goto('/');
    // The SPA renders a Live TV nav label in the tab bar.
    await expect(page).toHaveTitle(/live tv|livetv|continuum/i);
  });

  test('api: add M3U + XMLTV sources, refresh, channels and EPG visible', async ({ request }) => {
    const m3uURL = process.env.E2E_M3U_URL;
    const xmltvURL = process.env.E2E_XMLTV_URL;
    expect(m3uURL, 'globalSetup must publish E2E_M3U_URL').toBeTruthy();
    expect(xmltvURL, 'globalSetup must publish E2E_XMLTV_URL').toBeTruthy();

    // Create M3U source.
    const createM3U = await request.post('/api/v1/livetv/admin/sources/m3u', {
      data: {
        name: 'E2E M3U',
        url: m3uURL,
        http_headers: {},
        enabled: true,
        refresh_interval: '6h',
      },
    });
    expect(createM3U.ok(), `create m3u: ${await createM3U.text()}`).toBeTruthy();
    const m3u = await createM3U.json();
    expect(m3u.id).toBeTruthy();

    // Refresh M3U.
    const refreshM3U = await request.post(`/api/v1/livetv/admin/sources/m3u/${m3u.id}/refresh`);
    expect(refreshM3U.ok(), `refresh m3u: ${await refreshM3U.text()}`).toBeTruthy();

    // Wait for last_status='ok'.
    await expect
      .poll(
        async () => {
          const r = await request.get(`/api/v1/livetv/admin/sources/m3u/${m3u.id}`);
          if (!r.ok()) return 'http-' + r.status();
          const body = await r.json();
          return body.last_status as string;
        },
        { timeout: 15_000, intervals: [200, 500, 1000] },
      )
      .toBe('ok');

    // Create XMLTV source.
    const createXMLTV = await request.post('/api/v1/livetv/admin/sources/xmltv', {
      data: {
        name: 'E2E XMLTV',
        url: xmltvURL,
        gzip: false,
        enabled: true,
        refresh_interval: '3h',
      },
    });
    expect(createXMLTV.ok(), `create xmltv: ${await createXMLTV.text()}`).toBeTruthy();
    const xmltv = await createXMLTV.json();
    const refreshXMLTV = await request.post(
      `/api/v1/livetv/admin/sources/xmltv/${xmltv.id}/refresh`,
    );
    expect(refreshXMLTV.ok(), `refresh xmltv: ${await refreshXMLTV.text()}`).toBeTruthy();

    await expect
      .poll(
        async () => {
          const r = await request.get(`/api/v1/livetv/admin/sources/xmltv/${xmltv.id}`);
          if (!r.ok()) return 'http-' + r.status();
          const body = await r.json();
          return body.last_status as string;
        },
        { timeout: 15_000, intervals: [200, 500, 1000] },
      )
      .toBe('ok');

    // Channel list now contains E2E Channel One.
    const channels = await request.get('/api/v1/livetv/channels');
    expect(channels.ok()).toBeTruthy();
    const channelsBody = await channels.json();
    const list = channelsBody.data ?? channelsBody;
    const names = (Array.isArray(list) ? list : []).map(
      (c: { display_name?: string }) => c.display_name,
    );
    expect(names).toContain('E2E Channel One');

    // Guide window includes the E2E Test Show program.
    const now = new Date();
    const startMs = now.getTime() - 5 * 60_000;
    const endMs = now.getTime() + 30 * 60_000;
    const guide = await request.get(
      `/api/v1/livetv/guide?start=${new Date(startMs).toISOString()}&end=${new Date(endMs).toISOString()}`,
    );
    expect(guide.ok(), `guide: ${await guide.text()}`).toBeTruthy();
    const guideText = await guide.text();
    expect(guideText).toContain('E2E Test Show');
  });

  test.skip('happy-path: admin adds sources, user watches a channel', async ({ page }) => {
    await page.goto('/admin/sources');

    // Add M3U
    await page.getByRole('button', { name: /add m3u/i }).click();
    await page.getByLabel('Name').fill('E2E M3U');
    await page.getByLabel('URL').fill(process.env.E2E_M3U_URL!);
    await page.getByRole('button', { name: /save/i }).click();
    await page.getByRole('button', { name: /refresh/i }).first().click();
    await expect(page.getByText(/ok/i).first()).toBeVisible({ timeout: 10_000 });

    // Add XMLTV
    await page.getByRole('tab', { name: /xmltv/i }).click();
    await page.getByRole('button', { name: /add xmltv/i }).click();
    await page.getByLabel('Name').fill('E2E XMLTV');
    await page.getByLabel('URL').fill(process.env.E2E_XMLTV_URL!);
    await page.getByRole('button', { name: /save/i }).click();
    await page.getByRole('button', { name: /refresh/i }).first().click();
    await expect(page.getByText(/ok/i).first()).toBeVisible({ timeout: 10_000 });

    // Channels
    await page.goto('/channels');
    await expect(page.getByText('E2E Channel One')).toBeVisible({ timeout: 5_000 });

    // Guide
    await page.goto('/guide');
    await expect(page.getByText('E2E Test Show')).toBeVisible({ timeout: 5_000 });

    // Play (will fail to actually demux random bytes, but the route + the
    // cookie + the video element load).
    await page.getByText('E2E Channel One').click();
    await expect(page.locator('video')).toBeVisible();
  });
});
