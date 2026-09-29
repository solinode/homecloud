"use client"

import { StatusBadge } from "@/components/console/status-badge"
import { useApi } from "@/lib/hooks"
import type { SsmParameter, SsmParameterType } from "@/lib/types"

export const SSM_PATH = "/api/v1/ssm"
export const PARAMETERS_PATH = `${SSM_PATH}/parameters`
/** Single-parameter routes take the name in ?name= because names contain "/". */
export const PARAMETER_PATH = `${SSM_PATH}/parameter`

export const DEFAULT_KEY_ALIAS = "alias/hc/ssm"
export const MAX_VALUE = 8192
export const MAX_NAME = 1011

export const parameterHref = (name: string, tab?: string) => `/ssm/parameter/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const editParameterHref = (name: string) => `/ssm/create/?name=${encodeURIComponent(name)}`

const NAME_RE = /^\/?[a-zA-Z0-9_.\-/]+$/

/** nameError mirrors the checks in ssm.go put(); returns null when valid. */
export function nameError(name: string): string | null {
  if (!name) return "Enter a parameter name."
  if (name.length > MAX_NAME) return `Names are at most ${MAX_NAME} characters.`
  if (!NAME_RE.test(name)) return "Use letters, digits and the characters _ . - /"
  if (name.includes("//")) return "Names can't contain empty path segments (//)."
  if (name.includes("/") && !name.startsWith("/")) return "Hierarchical names must start with / (e.g. /app/db/url)."
  if (name.endsWith("/")) return "Names can't end with /."
  return null
}

export const TYPE_TONE: Record<SsmParameterType, "neutral" | "info" | "warning"> = { String: "neutral", StringList: "info", SecureString: "warning" }

export function TypeBadge({ type }: { type: SsmParameterType | string }) {
  return <StatusBadge status={type} label={type} tone={TYPE_TONE[type as SsmParameterType] ?? "neutral"} />
}

/**
 * useParameter loads a parameter summary (description, KMS key, ...). GetParameter
 * doesn't return those fields, so the summary comes from DescribeParameters
 * filtered by prefix. `notFound` is true once loaded without an exact match.
 */
export function useParameter(name: string) {
  const r = useApi<SsmParameter[]>(name ? PARAMETERS_PATH : null, { query: { prefix: name }, refreshInterval: 15_000 })
  const param = r.data?.find((p) => p.name === name)
  return { ...r, param, notFound: !!r.data && !param }
}

/** Splits a StringList value into its items. */
export const listItems = (v: string) => v.split(",").map((s) => s.trim())
