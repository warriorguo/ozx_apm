import { NavLink, Outlet } from 'react-router-dom'

const navItems = [
  { path: '/', label: 'Dashboard' },
  { path: '/performance', label: 'Performance' },
  { path: '/crashes', label: 'Crashes' },
  { path: '/exceptions', label: 'Exceptions' },
]

export function Layout() {
  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-slate-800 text-white px-6 py-4">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-bold">OZX APM</h1>
          <nav className="flex gap-6">
            {navItems.map((item) => (
              <NavLink
                key={item.path}
                to={item.path}
                className={({ isActive }) =>
                  `text-sm ${isActive ? 'text-white font-medium' : 'text-slate-300 hover:text-white'}`
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>
      <main className="flex-1 p-6">
        <Outlet />
      </main>
    </div>
  )
}
