import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider, createRouter, createRootRoute, createRoute, Outlet,
} from '@tanstack/react-router'
import { queryClient } from './api'
import type { AlertsSearch, ObjectsSearch } from './types'
import { Layout } from './components/Layout'
import { ErrorState } from './components/ui'
import { t } from './i18n'
import { OverviewPage } from './pages/Overview'
import { ProblemsPage } from './pages/Problems'
import { ObjectsPage, ObjectDetailPage } from './pages/Objects'
import { AlertsPage, IncidentsPage } from './pages/Alerts'
import { EventsPage } from './pages/Events'
import { OnCallPage } from './pages/OnCall'
import { AdminPage } from './pages/Admin'
import { AlertingConfigPage } from './pages/AlertingConfig'
import { DashboardsPage, DashboardViewPage } from './pages/Dashboards'
import { ReportsPage } from './pages/Reports'
import { BusinessPage } from './pages/Business'
import { DiscoveryPage } from './pages/Discovery'
import { MaintenancePage } from './pages/Maintenance'
import { TemplatesPage } from './pages/Templates'
import './index.css'

const rootRoute = createRootRoute({
  component: () => <Layout><Outlet /></Layout>,
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

// PWA service worker (ADR-12: shell caching + push-ready)
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  navigator.serviceWorker.register('/sw.js').catch(() => {})
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
