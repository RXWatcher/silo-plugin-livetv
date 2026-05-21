// Playwright globalSetup wires together:
//   1. A Postgres container (or a pre-existing DATABASE_URL) with the
//      livetv schema pre-created.
//   2. The livetv-e2e-server Go binary, bound to a fresh ephemeral port.
//   3. The fixture HTTP server serving M3U + XMLTV + a stub HLS playlist.
//
// The URLs are exposed via process.env so individual specs (and Playwright's
// `use.baseURL`) can pick them up:
//
//   E2E_BASE_URL   - the livetv-e2e-server's HTTP base URL.
//   E2E_M3U_URL    - the fixture M3U URL the admin form should be filled with.
//   E2E_XMLTV_URL  - the fixture XMLTV URL.
//
// globalTeardown stops the e2e server, the fixture server, and the postgres
// container in reverse order.
//
// To skip the container and use an existing Postgres set DATABASE_URL in the
// environment when launching playwright. The schema `livetv` must already
// exist and be owned by the role in the DSN.

import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import type { FullConfig } from '@playwright/test';
import { startE2EServer, type E2EServerHandle } from './server.js';
import { startFixtureServer, type FixtureServer } from './fixtureServer.js';

const execFileP = promisify(execFile);

interface SetupState {
  pgContainer?: string;
  serverHandle?: E2EServerHandle;
  fixtureHandle?: FixtureServer;
}

const state: SetupState = {};
// eslint-disable-next-line @typescript-eslint/no-explicit-any
(globalThis as any).__livetvE2E = state;

async function startPostgresContainer(): Promise<{ dsn: string; containerID: string }> {
  // Start a one-shot postgres:16 container with the livetv role + schema
  // pre-created via POSTGRES_DB / init.
  const { stdout } = await execFileP('docker', [
    'run',
    '-d',
    '--rm',
    '-e',
    'POSTGRES_USER=plugin_livetv',
    '-e',
    'POSTGRES_PASSWORD=e2e',
    '-e',
    'POSTGRES_DB=continuum',
    '-p',
    '0:5432',
    'postgres:16',
  ]);
  const containerID = stdout.trim();

  // Find the host port the container ephemeral port was mapped to.
  const { stdout: portOut } = await execFileP('docker', [
    'port',
    containerID,
    '5432/tcp',
  ]);
  // Output looks like `0.0.0.0:54321\n[::]:54321`. Take the first IPv4 line.
  const portMatch = portOut.split('\n')[0].match(/:(\d+)/);
  if (!portMatch) throw new Error(`could not parse docker port: ${portOut}`);
  const port = Number(portMatch[1]);

  // Wait for pg_isready to succeed and the role+db to actually accept
  // connections. POSTGRES_DB is created by the entrypoint AFTER the first
  // pg_isready response, so we additionally try `psql -c SELECT 1` until it
  // works. Poll for up to 60s.
  const start = Date.now();
  let ready = false;
  while (Date.now() - start < 60_000) {
    try {
      await execFileP('docker', [
        'exec',
        containerID,
        'pg_isready',
        '-U',
        'plugin_livetv',
        '-d',
        'continuum',
      ]);
      await execFileP('docker', [
        'exec',
        containerID,
        'psql',
        '-U',
        'plugin_livetv',
        '-d',
        'continuum',
        '-c',
        'SELECT 1',
      ]);
      ready = true;
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  if (!ready) {
    throw new Error('postgres container never accepted connections');
  }
  // Create the livetv schema.
  await execFileP('docker', [
    'exec',
    containerID,
    'psql',
    '-U',
    'plugin_livetv',
    '-d',
    'continuum',
    '-c',
    'CREATE SCHEMA IF NOT EXISTS livetv AUTHORIZATION plugin_livetv;',
  ]);

  const dsn = `postgres://plugin_livetv:e2e@127.0.0.1:${port}/continuum?sslmode=disable&search_path=livetv`;
  return { dsn, containerID };
}

export default async function globalSetup(_config: FullConfig): Promise<void> {
  let databaseURL = process.env.DATABASE_URL ?? '';
  if (!databaseURL) {
    const pg = await startPostgresContainer();
    databaseURL = pg.dsn;
    state.pgContainer = pg.containerID;
  }

  state.fixtureHandle = await startFixtureServer({ port: 0 });
  state.serverHandle = await startE2EServer({ databaseURL });

  process.env.E2E_BASE_URL = state.serverHandle.baseURL;
  process.env.E2E_M3U_URL = state.fixtureHandle.m3uURL;
  process.env.E2E_XMLTV_URL = state.fixtureHandle.xmltvURL;
}
