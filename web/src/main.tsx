import { StrictMode, Suspense, lazy } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider, createRouter, createRootRoute, createRoute, Outlet,
} from '@tanstack/react-router'
import { queryClient } from './api'
import type { AlertsSearch, ObjectsSearch } from './types'
import { Layout } from './components/Layout'
import { ErrorState, Spinner } from '@/components/kit'
import { t } from './i18n'
import './index.css'
// Side-effect import: applies the saved colour theme (<html data-theme>)
// before first paint so there's no flash of the default palette.
import './theme'

// Route-level code splitting (SPEC §12.2): each page is a separate chunk
// loaded on demand. In dev this means visiting a route only transforms that
// route's module graph instead of all ~14 pages (and uplot/charts) up front.
const OverviewPage = lazy(() => import('./pages/Overview').then((m) => ({ default: m.OverviewPage })))
const ProblemsPage = lazy(() => import('./pages/Problems').then((m) => ({ default: m.ProblemsPage })))
const ObjectsPage = lazy(() => import('./pages/Objects').then((m) => ({ default: m.ObjectsPage })))
const ObjectDetailPage = lazy(() => import('./pages/Objects').then((m) => ({ default: m.ObjectDetailPage })))
const AlertsPage = lazy(() => import('./pages/Alerts').then((m) => ({ default: m.AlertsPage })))
const IncidentsPage = lazy(() => import('./pages/Alerts').then((m) => ({ default: m.IncidentsPage })))
const EventsPage = lazy(() => import('./pages/Events').then((m) => ({ default: m.EventsPage })))
const OnCallPage = lazy(() => import('./pages/OnCall').then((m) => ({ default: m.OnCallPage })))
const AdminPage = lazy(() => import('./pages/Admin').then((m) => ({ default: m.AdminPage })))
const AlertingConfigPage = lazy(() => import('./pages/AlertingConfig').then((m) => ({ default: m.AlertingConfigPage })))
const DashboardsPage = lazy(() => import('./pages/Dashboards').then((m) => ({ default: m.DashboardsPage })))
const DashboardViewPage = lazy(() => import('./pages/Dashboards').then((m) => ({ default: m.DashboardViewPage })))
const ReportsPage = lazy(() => import('./pages/Reports').then((m) => ({ default: m.ReportsPage })))
const BusinessPage = lazy(() => import('./pages/Business').then((m) => ({ default: m.BusinessPage })))
const DiscoveryPage = lazy(() => import('./pages/Discovery').then((m) => ({ default: m.DiscoveryPage })))
const MaintenancePage = lazy(() => import('./pages/Maintenance').then((m) => ({ default: m.MaintenancePage })))
const TemplatesPage = lazy(() => import('./pages/Templates').then((m) => ({ default: m.TemplatesPage })))

const rootRoute = createRootRoute({
  component: () => (
    <Layout>
      <Suspense fallback={<div className="p-8"><Spinner /></div>}>
        <Outlet />
      </Suspense>
    </Layout>
  ),
})

// str narrows an unknown search param to a non-empty string.
const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined)

const routes = [
  createRoute({ getParentRoute: () => rootRoute, path: '/', component: OverviewPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/problems', component: ProblemsPage }),
  createRoute({
    getParentRoute: () => rootRoute, path: '/objects', component: ObjectsPage,
    // Filters live in the URL (linkable views, drill-down targets,
    // back-button restores state). Keys stay optional so plain links
    // to /objects need no search object.
    validateSearch: (s: Record<string, unknown>): ObjectsSearch => {
      const out: ObjectsSearch = {}
      const selector = str(s.selector); if (selector) out.selector = selector
      const q = str(s.q); if (q) out.q = q
      const state = str(s.state); if (state) out.state = state
      if (s.kind === 'host' || s.kind === 'service') out.kind = s.kind
      return out
    },
  }),
  createRoute({ getParentRoute: () => rootRoute, path: '/objects/$id', component: ObjectDetailPage }),
  createRoute({
    getParentRoute: () => rootRoute, path: '/alerts', component: AlertsPage,
    validateSearch: (s: Record<string, unknown>): AlertsSearch => {
      const out: AlertsSearch = {}
      const status = str(s.status); if (status) out.status = status
      const severity = str(s.severity); if (severity) out.severity = severity
      return out
    },
  }),
  createRoute({ getParentRoute: () => rootRoute, path: '/incidents', component: IncidentsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/events', component: EventsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/oncall', component: OnCallPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/alerting', component: AlertingConfigPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/dashboards', component: DashboardsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/dashboards/$name', component: DashboardViewPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/reports', component: ReportsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/business', component: BusinessPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/discovery', component: DiscoveryPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/maintenance', component: MaintenancePage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/templates', component: TemplatesPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/admin', component: AdminPage }),
]

const router = createRouter({
  routeTree: rootRoute.addChildren(routes),
  // A failed route render shows the shared error UI (with retry) instead
  // of a blank screen; unknown paths get a real 404.
  defaultErrorComponent: ({ error, reset }) => (
    <div className="p-8"><ErrorState error={error} onRetry={reset} /></div>
  ),
  defaultNotFoundComponent: () => (
    <div className="flex flex-col items-center justify-center gap-2 p-12 text-center">
      <div className="text-4xl font-bold text-muted-foreground">404</div>
      <div className="text-sm text-muted-foreground">{window.location.pathname}</div>
      <a href="/" className="text-sm text-primary hover:underline mt-1">{t('overview')}</a>
    </div>
  ),
})

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

// No service worker. A monitoring console has no offline use case, and the
// previous caching worker wedged the app by serving a stale shell. Actively
// unregister any worker a prior build installed; the served /sw.js is now a
// self-destroying stub that cleans up browsers still controlled by the old one.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.getRegistrations()
    .then((regs) => regs.forEach((r) => r.unregister()))
    .catch(() => {})
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
