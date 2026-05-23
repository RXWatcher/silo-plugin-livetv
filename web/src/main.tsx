import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { App } from './App';
import { makeQueryClient } from './lib/queryClient';
import { mountPath } from './lib/mountPath';
import './index.css';

const queryClient = makeQueryClient();

// Silo mounts each plugin SPA at /api/v1/plugins/{installationId}/...
// We feed react-router the same basename so internal links don't drop the
// proxy prefix. In dev mode mountPath() returns '' and basename falls back
// to '/'.
const basename = mountPath() || '/';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={basename}>
        <App />
        <Toaster richColors theme="dark" />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
