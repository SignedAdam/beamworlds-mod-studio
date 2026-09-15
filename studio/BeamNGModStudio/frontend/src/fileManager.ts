import { useEffect, useState } from 'react'
import { AppService as API } from '../bindings/github.com/SignedAdam/beamng-mod-studio/index.js'

// The wording is platform native ("Open in Explorer" on Windows, "Reveal in
// Finder" on macOS), so it is read once from the backend and shared by every
// menu that reveals a file.
const fallbackLabel = 'Open in file manager'
let pending: Promise<string> | null = null

export function fileManagerActionLabel(): Promise<string> {
  pending ??= Promise.resolve(API.FileManagerActionLabel()).catch(() => fallbackLabel)
  return pending
}

export function useFileManagerLabel(): string {
  const [label, setLabel] = useState(fallbackLabel)
  useEffect(() => {
    let active = true
    void fileManagerActionLabel().then(value => {
      if (active && value.trim()) setLabel(value)
    })
    return () => {
      active = false
    }
  }, [])
  return label
}
