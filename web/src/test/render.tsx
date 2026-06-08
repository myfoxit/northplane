// Test render helpers: a fresh QueryClient per render (no cross-test cache
// bleed, retries off so error paths resolve immediately) plus a minimal
// in-memory TanStack Router so components that use <Link> mount without the
// full app route tree.
import type { ReactElement, ReactNode } from 'react'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider, createRouter, createRootRoute, createRoute,
  createMemoryHistory, Outlet,
} from '@tanstack/react-router'

function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
}

// Mount `ui` under a router whose root route renders it. A couple of stub
// routes are registered so any <Link to="/objects/$id"> etc. resolves.
export function renderWithProviders(ui: ReactElement) {
  const qc = makeQueryClient()
  const rootRoute = createRootRoute({
    component: () => (
      <QueryClientProvider client={qc}>
        <Outlet />
      </QueryClientProvider>
    ),
  })
  const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: () => ui })
  const stubRoutes = ['/objects', '/objects/$id', '/alerts', '/incidents'].map((path) =>
    createRoute({ getParentRoute: () => rootRoute, path, component: () => null }),
  )
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, ...stubRoutes]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return {
    qc,
    ...render(<RouterProvider router={router} /> as ReactNode as ReactElement),
  }
}
