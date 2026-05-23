// Launches the livetv-e2e-server Go binary, reads E2E_PORT off stdout, and
// returns the bound base URL + a stop() for teardown. Playwright's webServer
// reads stdout but does not give us the port; we use this helper directly
// from globalSetup to publish the port via process.env.
//
// Required env when calling start():
//   DATABASE_URL  - Postgres DSN the harness should use.
//
// The binary is expected to live at the repo root as ./livetv-e2e-server
// (built by `go build ./cmd/livetv-e2e-server`). globalSetup builds it on
// demand if missing.

import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileP = promisify(execFile);

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// /opt/silo_plugins/silo-plugin-livetv
export const REPO_ROOT = resolve(__dirname, '..', '..', '..');
export const E2E_BINARY = resolve(REPO_ROOT, 'livetv-e2e-server');

export interface E2EServerHandle {
  baseURL: string;
  port: number;
  stop: () => Promise<void>;
}

export async function buildE2EBinary(): Promise<void> {
  if (existsSync(E2E_BINARY)) return;
  await execFileP('go', ['build', '-o', E2E_BINARY, './cmd/livetv-e2e-server'], {
    cwd: REPO_ROOT,
  });
}

export async function startE2EServer(opts: {
  databaseURL: string;
  host?: string;
  userID?: string;
}): Promise<E2EServerHandle> {
  await buildE2EBinary();

  const child: ChildProcess = spawn(E2E_BINARY, [], {
    env: {
      ...process.env,
      DATABASE_URL: opts.databaseURL,
      E2E_LISTEN_HOST: opts.host ?? '127.0.0.1',
      E2E_USER_ID: opts.userID ?? 'e2e-user',
    },
    stdio: ['ignore', 'pipe', 'inherit'],
  });

  const baseURL = await new Promise<string>((resolveURL, rejectURL) => {
    let buf = '';
    const onData = (chunk: Buffer) => {
      buf += chunk.toString('utf8');
      const match = buf.match(/^E2E_BASE_URL=(\S+)/m);
      if (match) {
        child.stdout?.off('data', onData);
        resolveURL(match[1]);
      }
    };
    child.stdout?.on('data', onData);
    child.once('error', rejectURL);
    child.once('exit', (code) => {
      if (code !== 0) rejectURL(new Error(`e2e server exited early with code ${code}`));
    });
    setTimeout(() => rejectURL(new Error('timeout waiting for E2E_BASE_URL')), 30_000);
  });

  const port = Number(new URL(baseURL).port);

  return {
    baseURL,
    port,
    stop: () =>
      new Promise<void>((resolveStop) => {
        if (!child.pid || child.exitCode !== null) {
          resolveStop();
          return;
        }
        child.once('exit', () => resolveStop());
        child.kill('SIGTERM');
        // hard kill after 5s if it doesn't shut down
        setTimeout(() => {
          if (child.exitCode === null) child.kill('SIGKILL');
        }, 5_000);
      }),
  };
}
