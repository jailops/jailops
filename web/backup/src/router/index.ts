import { createRouter, createWebHistory, type RouteRecordRaw, type Router, type RouterHistory } from 'vue-router'
import { guard } from '@jailops/shared/router'

export const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true, title: 'Sign in' } },
  { path: '/', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: 'Overview' } },
  { path: '/hosts', name: 'hosts', component: () => import('@/views/HostsView.vue'), meta: { title: 'Hosts' } },
  { path: '/snapshots', name: 'snapshots', component: () => import('@/views/SnapshotsView.vue'), meta: { title: 'Snapshots' } },
  { path: '/deletions', name: 'deletions', component: () => import('@/views/DeletionsView.vue'), meta: { title: 'Deletions' } },
  { path: '/retention', name: 'retention', component: () => import('@/views/RetentionView.vue'), meta: { title: 'Retention' } },
  { path: '/maintenance', name: 'maintenance', component: () => import('@/views/MaintenanceView.vue'), meta: { title: 'Maintenance' } },
  { path: '/audit', name: 'audit', component: () => import('@/views/AuditView.vue'), meta: { title: 'Audit log' } },
  { path: '/access', name: 'access', component: () => import('@/views/AccessView.vue'), meta: { title: 'Access' } },
  { path: '/:rest(.*)*', name: 'notfound', component: () => import('@/views/NotFoundView.vue'), meta: { title: 'Not found' } },
]

export function makeRouter(history: RouterHistory = createWebHistory()): Router {
  const r = createRouter({ history, routes, scrollBehavior: () => ({ top: 0 }) })
  r.beforeEach((to) => guard(to))
  r.afterEach((to) => {
    document.title = `${to.meta.title ?? ''} - JailOps backup node console`.replace(/^ - /, '')
  })
  return r
}
