import { useState } from 'react'
import { getStoredApiKey, setStoredApiKey, clearStoredApiKey } from '@/api/client'

/**
 * Manages the API key lifecycle: read from sessionStorage, set, clear.
 * Components that need auth-gate logic import this hook.
 */
export function useAPIKey() {
  const [apiKey, setApiKeyState] = useState<string | null>(getStoredApiKey)

  function setApiKey(key: string) {
    setStoredApiKey(key)
    setApiKeyState(key)
  }

  function clearApiKey() {
    clearStoredApiKey()
    setApiKeyState(null)
  }

  return { apiKey, setApiKey, clearApiKey, isAuthenticated: !!apiKey }
}
