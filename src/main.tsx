import React, { lazy, Suspense } from 'react'
import ReactDOM from 'react-dom/client'

const RuntimeApp = import.meta.env.MODE === 'web'
  ? lazy(() => import('./components/WebMacWorkspace').then((module) => ({ default: module.WebMacWorkspace })))
  : lazy(() => import('./TauriIdeApp'))

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <Suspense fallback={<div style={{ minHeight: '100vh', background: '#0d1015' }} />}>
      <RuntimeApp />
    </Suspense>
  </React.StrictMode>,
)
