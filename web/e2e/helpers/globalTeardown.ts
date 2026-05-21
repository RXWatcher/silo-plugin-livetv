// Cleans up resources created in globalSetup. Errors are swallowed: a
// teardown failure should not mask a test failure.

import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import type { FullConfig } from '@playwright/test';
import type { E2EServerHandle } from './server.js';
import type { FixtureServer } from './fixtureServer.js';

const execFileP = promisify(execFile);

interface SetupState {
  pgContainer?: string;
  serverHandle?: E2EServerHandle;
  fixtureHandle?: FixtureServer;
}

export default async function globalTeardown(_config: FullConfig): Promise<void> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const state = (globalThis as any).__livetvE2E as SetupState | undefined;
  if (!state) return;
  if (state.serverHandle) {
    try {
      await state.serverHandle.stop();
    } catch {
      /* ignore */
    }
  }
  if (state.fixtureHandle) {
    try {
      await state.fixtureHandle.stop();
    } catch {
      /* ignore */
    }
  }
  if (state.pgContainer) {
    try {
      await execFileP('docker', ['stop', state.pgContainer]);
    } catch {
      /* ignore */
    }
  }
}
