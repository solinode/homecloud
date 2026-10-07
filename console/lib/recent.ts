"use client"

import { useEffect, useState } from "react"

import { serviceForPath } from "@/lib/services"

/**
 * Recently visited resources (detail pages), kept per browser in localStorage
 * for the command palette. Purely a convenience: every access is guarded and
 * the console works the same when storage is unavailable.
 */
export interface RecentResource {
  href: string
  label: string
  kind: string
  service: string
  at: number
}

const KEY = "hc.recent.v1"
const MAX = 8
// Query parameters that identify a resource on a detail page, most specific first.
const ID_PARAMS = ["id", "name", "arn", "bucket", "key", "zone", "pool", "group", "table", "queue", "topic", "function", "repository", "stack"]

const listeners = new Set<() => void>()

export function readRecent(): RecentResource[] {
  try {
    const raw = window.localStorage.getItem(KEY)
    const v = raw ? (JSON.parse(raw) as RecentResource[]) : []
    return Array.isArray(v) ? v.slice(0, MAX) : []
  } catch {
    return []
  }
}

/** recordVisit stores a detail-page visit ("/ec2/instance/?id=i-123"). List pages are ignored. */
export function recordVisit(pathname: string, search: string) {
  const params = new URLSearchParams(search)
  const param = ID_PARAMS.find((p) => params.get(p))
  if (!param) return
  const svc = serviceForPath(pathname)
  if (!svc) return
  const segs = pathname.split("/").filter(Boolean)
  const kind = (segs[segs.length - 1] ?? svc.short).replace(/-/g, " ")
  const value = params.get(param) ?? ""
  const label = value.includes(":") && value.startsWith("arn:") ? value.split(/[:/]/).pop() || value : value
  const href = `${pathname}?${params.toString()}`
  try {
    const list = readRecent().filter((r) => r.href !== href)
    list.unshift({ href, label, kind, service: svc.name, at: Date.now() })
    window.localStorage.setItem(KEY, JSON.stringify(list.slice(0, MAX)))
    listeners.forEach((l) => l())
  } catch {
    // storage blocked: nothing to remember
  }
}

export function useRecentResources(): RecentResource[] {
  const [list, setList] = useState<RecentResource[]>([])
  useEffect(() => {
    const update = () => setList(readRecent())
    update()
    listeners.add(update)
    return () => {
      listeners.delete(update)
    }
  }, [])
  return list
}
