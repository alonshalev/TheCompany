/**
 * useRunStream — live SSE consumer for a workflow/agent run.
 *
 * Connects to GET /v1/projects/:projectID/runs/:runID/stream using a
 * manual fetch + ReadableStream so we can attach the Bearer token.
 * (The native EventSource API doesn't support custom headers.)
 *
 * Returns:
 *   events  — accumulated RunEvent array, ordered by seq
 *   status  — 'idle' | 'connecting' | 'connected' | 'closed' | 'error'
 *   error   — last error message if status === 'error'
 */

import { useEffect, useRef, useState } from 'react'
import { type RunEvent } from '@/api/client'
import { getStoredApiKey } from '@/api/client'

export type StreamStatus = 'idle' | 'connecting' | 'connected' | 'closed' | 'error'

interface UseRunStreamResult {
  events: RunEvent[]
  status: StreamStatus
  error: string | null
}

const API_BASE = import.meta.env.VITE_API_BASE ?? ''

export function useRunStream(
  projectID: string | null,
  runID: string | null,
  /** Stop streaming once the run reaches a terminal status */
  enabled = true,
): UseRunStreamResult {
  const [events, setEvents] = useState<RunEvent[]>([])
  const [status, setStatus] = useState<StreamStatus>('idle')
  const [error, setError] = useState<string | null>(null)

  // Keep a ref so the cleanup function in useEffect can abort the fetch
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!projectID || !runID || !enabled) {
      setStatus('idle')
      return
    }

    // Reset state each time the run changes
    setEvents([])
    setError(null)
    setStatus('connecting')

    const controller = new AbortController()
    abortRef.current = controller

    async function stream() {
      const url = `${API_BASE}/v1/projects/${projectID}/runs/${runID}/stream`
      const apiKey = getStoredApiKey()

      try {
        const res = await fetch(url, {
          method: 'GET',
          headers: {
            Accept: 'text/event-stream',
            ...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}),
          },
          signal: controller.signal,
        })

        if (!res.ok) {
          const body = await res.text().catch(() => res.statusText)
          setStatus('error')
          setError(`HTTP ${res.status}: ${body}`)
          return
        }

        if (!res.body) {
          setStatus('error')
          setError('Response has no body')
          return
        }

        setStatus('connected')

        const reader = res.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        // eslint-disable-next-line no-constant-condition
        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })

          // SSE messages are separated by double newlines.
          // Split on \n\n, keeping the last (possibly partial) chunk in buffer.
          const parts = buffer.split('\n\n')
          buffer = parts.pop() ?? ''

          for (const part of parts) {
            // Each SSE message may have multiple lines; we only handle "data:" lines.
            const dataLines = part
              .split('\n')
              .filter(l => l.startsWith('data:'))
              .map(l => l.slice(5).trim())

            for (const json of dataLines) {
              if (!json || json === '[DONE]') continue
              try {
                const evt = JSON.parse(json) as RunEvent
                setEvents(prev => {
                  // Deduplicate by id and keep sorted by seq
                  const exists = prev.some(e => e.id === evt.id)
                  if (exists) return prev
                  return [...prev, evt].sort((a, b) => a.seq - b.seq)
                })
              } catch {
                // malformed JSON — skip
              }
            }
          }
        }

        // Stream ended cleanly
        setStatus('closed')
      } catch (err) {
        if ((err as Error).name === 'AbortError') {
          // Component unmounted or run changed — expected, not an error
          return
        }
        setStatus('error')
        setError((err as Error).message ?? 'Stream error')
      }
    }

    stream()

    return () => {
      controller.abort()
    }
  }, [projectID, runID, enabled])

  return { events, status, error }
}
