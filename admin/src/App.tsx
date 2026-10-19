import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Dashboard } from './pages/Dashboard'
import { Performance } from './pages/Performance'
import { Crashes } from './pages/Crashes'
import { CrashDetail } from './pages/CrashDetail'
import { Exceptions } from './pages/Exceptions'
import { Context } from './pages/Context'
import { Custom } from './pages/Custom'
import { ExceptionDetail } from './pages/ExceptionDetail'
import { Sessions } from './pages/Sessions'
import { Memory } from './pages/Memory'
import { Health } from './pages/Health'
import { Funnel } from './pages/Funnel'
import { FiltersProvider } from './hooks/useFilters'

function App() {
  return (
    <BrowserRouter>
      <FiltersProvider>
        <Routes>
          <Route path="/" element={<Layout />}>
            <Route index element={<Dashboard />} />
            <Route path="performance" element={<Performance />} />
            <Route path="crashes" element={<Crashes />} />
            <Route path="crashes/detail" element={<CrashDetail />} />
            <Route path="exceptions" element={<Exceptions />} />
            <Route path="exceptions/detail" element={<ExceptionDetail />} />
            <Route path="sessions" element={<Sessions />} />
            <Route path="context" element={<Context />} />
            <Route path="custom" element={<Custom />} />
            <Route path="memory" element={<Memory />} />
            <Route path="health" element={<Health />} />
            <Route path="funnel" element={<Funnel />} />
          </Route>
        </Routes>
      </FiltersProvider>
    </BrowserRouter>
  )
}

export default App
