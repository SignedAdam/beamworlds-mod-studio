import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

type InterfaceSize = 'compact' | 'default' | 'comfortable' | 'large'
type TextSize = 'small' | 'default' | 'large' | 'extra-large'

const INTERFACE_SIZE_STORAGE_KEY = 'beamworlds.interface-size'
const TEXT_SIZE_STORAGE_KEY = 'beamworlds.text-size'

function readInterfaceSize(): InterfaceSize {
  try {
    const value = window.localStorage.getItem(INTERFACE_SIZE_STORAGE_KEY)
    if (value === 'compact' || value === 'default' || value === 'comfortable' || value === 'large') return value
  } catch {
    // Storage can be unavailable in a restricted webview; use the contract default.
  }
  return 'default'
}

function readTextSize(): TextSize {
  try {
    const value = window.localStorage.getItem(TEXT_SIZE_STORAGE_KEY)
    if (value === 'small' || value === 'default' || value === 'large' || value === 'extra-large') return value
  } catch {
    // Storage can be unavailable in a restricted webview; use the contract default.
  }
  return 'default'
}

const root = document.documentElement
root.dataset.interfaceSize = readInterfaceSize()
root.dataset.textSize = readTextSize()

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
