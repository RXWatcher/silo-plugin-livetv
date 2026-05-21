// Tiny HTTP server that vends the M3U, XMLTV, and stub HLS playlist files
// used by the Playwright happy-path spec. The XMLTV document is templated
// at request time so the test program is always "currently airing".
//
// Spawn with `startFixtureServer({ port: 0 })`; the returned object exposes
// the bound port, the base URL, and `stop()`.

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { randomBytes } from 'node:crypto';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const FIXTURES = join(__dirname, '..', 'fixtures');

const m3uTemplate = readFileSync(join(FIXTURES, 'test.m3u.tmpl'), 'utf8');

function isoTime(offsetMs: number): string {
  // XMLTV-style UTC stamp: YYYYMMDDhhmmss +0000.
  const d = new Date(Date.now() + offsetMs);
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${d.getUTCFullYear()}${pad(d.getUTCMonth() + 1)}${pad(d.getUTCDate())}` +
    `${pad(d.getUTCHours())}${pad(d.getUTCMinutes())}${pad(d.getUTCSeconds())} +0000`
  );
}

function xmltvDoc(): string {
  const start = isoTime(-60_000); // 1 minute ago
  const stop = isoTime(60 * 60_000); // 1 hour from now
  return `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="e2e1"><display-name>E2E Channel One</display-name></channel>
  <programme start="${start}" stop="${stop}" channel="e2e1">
    <title>E2E Test Show</title>
    <desc>An end-to-end test program.</desc>
  </programme>
</tv>
`;
}

function hlsPlaylist(hostPort: string): string {
  return [
    '#EXTM3U',
    '#EXT-X-VERSION:3',
    '#EXT-X-TARGETDURATION:2',
    '#EXT-X-MEDIA-SEQUENCE:0',
    '#EXTINF:2.0,',
    `http://${hostPort}/seg1.ts`,
    '#EXT-X-ENDLIST',
    '',
  ].join('\n');
}

export interface FixtureServer {
  port: number;
  baseURL: string;
  m3uURL: string;
  xmltvURL: string;
  stop: () => Promise<void>;
}

export async function startFixtureServer({
  port = 0,
  host = '127.0.0.1',
}: { port?: number; host?: string } = {}): Promise<FixtureServer> {
  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const url = new URL(req.url ?? '/', `http://${req.headers.host ?? `${host}:${port}`}`);
    const hostPort = req.headers.host ?? `${host}:${port}`;
    switch (url.pathname) {
      case '/m3u': {
        const body = m3uTemplate.replace('{{FIXTURE_HOST_PORT}}', hostPort);
        res.writeHead(200, { 'Content-Type': 'application/x-mpegurl' });
        res.end(body);
        return;
      }
      case '/xmltv': {
        res.writeHead(200, { 'Content-Type': 'application/xml; charset=utf-8' });
        res.end(xmltvDoc());
        return;
      }
      case '/stream.m3u8': {
        res.writeHead(200, { 'Content-Type': 'application/vnd.apple.mpegurl' });
        res.end(hlsPlaylist(hostPort));
        return;
      }
      case '/seg1.ts': {
        // 1 KB of random bytes; the player will not demux this but the
        // <video> element will mount before failing.
        res.writeHead(200, { 'Content-Type': 'video/mp2t' });
        res.end(randomBytes(1024));
        return;
      }
      default:
        res.writeHead(404);
        res.end('not found');
    }
  });

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, host, () => resolve());
  });

  const addr = server.address();
  if (typeof addr === 'string' || addr === null) {
    throw new Error('fixture server did not bind a TCP port');
  }
  const bound = addr.port;
  const baseURL = `http://${host}:${bound}`;
  return {
    port: bound,
    baseURL,
    m3uURL: `${baseURL}/m3u`,
    xmltvURL: `${baseURL}/xmltv`,
    stop: () =>
      new Promise<void>((resolve) => {
        server.close(() => resolve());
      }),
  };
}
