import { createRouter, createWebHistory, type RouteRecordRaw, type Router, type RouterHistory } from 'vue-router'
import { guard } from '@jailops/shared/router'

export const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { public: true, title: 'Sign in' } },
  { path: '/', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: 'Overview' } },
  { path: '/:rest(.*)*', name: 'notfound', component: () => import('@/views/NotFoundView.vue'), meta: { title: 'Not found' } },
]

export function makeRouter(history: RouterHistory = createWebHistory()): Router {
  const r = createRouter({ history, routes, scrollBehavior: () => ({ top: 0 }) })
  r.beforeEach((to) => guard(to))
  r.afterEach((to) => {
    document.title = `${to.meta.title ?? ''} - __TITLE__`.replace(/^ - /, '')
  })
  return r
}
