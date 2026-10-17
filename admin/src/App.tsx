import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Dashboard } from './pages/Dashboard'
import { Performance } from './pages/Performance'
import { Crashes } from './pages/Crashes'
import { Exceptions } from './pages/Exceptions'
import { Context } from './pages/Context'
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
            <Route path="context" element={<Context />} />
          </Route>
        </Routes>
      </FiltersProvider>
    </BrowserRouter>
  )
}

export default App
