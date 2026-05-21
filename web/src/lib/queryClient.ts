import { QueryClient } from '@tanstack/react-query';

// Shared client construction so tests and main.tsx use the same defaults.
// Stale time is 30s by default — the home page and channel list refresh
// cheaply enough that we don't want them banging the API on every focus.
export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        retry: 1,
      },
    },
  });
}
