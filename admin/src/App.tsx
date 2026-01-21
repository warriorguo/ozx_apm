import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { Layout } from './components/Layout'
import { Dashboard } from './pages/Dashboard'
import { Performance } from './pages/Performance'
import { Crashes } from './pages/Crashes'
import { Exceptions } from './pages/Exceptions'

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Layout />}>
          <Route index element={<Dashboard />} />
          <Route path="performance" element={<Performance />} />
          <Route path="crashes" element={<Crashes />} />
          <Route path="exceptions" element={<Exceptions />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

export default App
