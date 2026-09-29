"use client"

import { useCallback, useEffect, useState } from "react"
import useSWR, { mutate as globalMutate, type SWRConfiguration } from "swr"
import { useRouter, useSearchParams } from "next/navigation"
import { toast } from "sonner"

import { ApiError, buildQuery, errorMessage, request, type Query } from "@/lib/api"

/** fetcher resolves SWR keys of the form "/api/v1/...?..." with a GET. */
export const fetcher = <T,>(key: string) => request<T>("GET", key)

export interface UseApiOptions<T> {
  query?: Query
  /** Poll interval in ms, or a function of the latest data (0 disables). */
  refreshInterval?: number | ((data: T | undefined) => number)
  keepPreviousData?: boolean
  revalidateOnFocus?: boolean
}

/**
 * useApi fetches (and caches) a GET endpoint. Pass null as the path to skip.
 * The returned mutate() re-fetches.
 */
export function useApi<T>(path: string | null, opts: UseApiOptions<T> = {}) {
  const key = path ? path + buildQuery(opts.query) : null
  const cfg: SWRConfiguration<T, ApiError> = {
    refreshInterval: opts.refreshInterval as SWRConfiguration<T>["refreshInterval"],
    keepPreviousData: opts.keepPreviousData ?? true,
    revalidateOnFocus: opts.revalidateOnFocus ?? true,
  }
  return useSWR<T, ApiError>(key, fetcher, cfg)
}

/** revalidate re-fetches every cached GET whose path starts with prefix. */
export function revalidate(prefix: string) {
  return globalMutate((key) => typeof key === "string" && key.startsWith(prefix))
}

export const swrConfig: SWRConfiguration = {
  fetcher,
  dedupingInterval: 1500,
  errorRetryCount: 2,
  shouldRetryOnError: (err: unknown) => !(err instanceof ApiError) || err.status === 0 || err.status >= 500,
}

/**
 * useAction wraps an async mutation: tracks pending state, toasts the API
 * error message on failure and an optional success message.
 */
export function useAction() {
  const [pending, setPending] = useState(false)
  const run = useCallback(async <R,>(fn: () => Promise<R>, success?: string | ((r: R) => string)): Promise<R | undefined> => {
    setPending(true)
    try {
      const r = await fn()
      if (success) toast.success(typeof success === "function" ? success(r) : success)
      return r
    } catch (e) {
      toast.error(errorMessage(e))
      return undefined
    } finally {
      setPending(false)
    }
  }, [])
  return { pending, run }
}

/** useQueryParam reads a search parameter (detail pages use ?id= / ?name=). */
export function useQueryParam(name: string): string {
  const sp = useSearchParams()
  return sp.get(name) ?? ""
}

/** useSetQueryParam updates a search parameter without adding a history entry. */
export function useSetQueryParam() {
  const router = useRouter()
  const sp = useSearchParams()
  return useCallback(
    (name: string, value: string | null) => {
      const p = new URLSearchParams(sp.toString())
      if (value === null || value === "") p.delete(name)
      else p.set(name, value)
      const qs = p.toString()
      router.replace(`${window.location.pathname}${qs ? `?${qs}` : ""}`, { scroll: false })
    },
    [router, sp],
  )
}

/** useNow re-renders every interval ms (for relative times). */
export function useNow(interval = 30_000) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), interval)
    return () => clearInterval(t)
  }, [interval])
  return now
}
