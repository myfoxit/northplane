import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider, createRouter, createRootRoute, createRoute, Outlet,
} from '@tanstack/react-router'
import { queryClient } from './api'
import { Layout } from './components/Layout'
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

const routes = [
  createRoute({ getParentRoute: () => rootRoute, path: '/', component: OverviewPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/problems', component: ProblemsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/objects', component: ObjectsPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/objects/$id', component: ObjectDetailPage }),
  createRoute({ getParentRoute: () => rootRoute, path: '/alerts', component: AlertsPage }),
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

const router = createRouter({ routeTree: rootRoute.addChildren(routes) })

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
