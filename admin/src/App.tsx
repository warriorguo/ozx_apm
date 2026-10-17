import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Dashboard } from './pages/Dashboard'
import { Performance } from './pages/Performance'
import { Crashes } from './pages/Crashes'
import { Exceptions } from './pages/Exceptions'
import { Context } from './pages/Context'
import { Custom } from './pages/Custom'
import { ExceptionDetail } from './pages/ExceptionDetail'
import { Sessions } from './pages/Sessions'
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
            <Route path="exceptions" element={<Exceptions />} />
            <Route path="exceptions/detail" element={<ExceptionDetail />} />
            <Route path="sessions" element={<Sessions />} />
            <Route path="context" element={<Context />} />
            <Route path="custom" element={<Custom />} />
          </Route>
        </Routes>
      </FiltersProvider>
    </BrowserRouter>
  )
}

export default App
