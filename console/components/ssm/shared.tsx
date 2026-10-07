"use client"

import { Tag, type TagAccent } from "@/components/console/tag"
import { useApi } from "@/lib/hooks"
import type { SsmDataType, SsmParameter, SsmParameterTier, SsmParameterType } from "@/lib/types"

export const SSM_PATH = "/api/v1/ssm"
export const PARAMETERS_PATH = `${SSM_PATH}/parameters`
/** Single-parameter routes take the name in ?name= because names contain "/". */
export const PARAMETER_PATH = `${SSM_PATH}/parameter`

/** The Parameter Store default key (AWS name); it resolves to the HomeCloud managed alias/hc/ssm key. */
export const DEFAULT_KEY_ALIAS = "alias/aws/ssm"
export const DEFAULT_KEY_ALIASES = ["alias/aws/ssm", "alias/hc/ssm"]
export const MAX_VALUE = 8192
export const MAX_STANDARD = 4096

export const TIERS: { tier: SsmParameterTier; title: string; blurb: string }[] = [
  { tier: "Standard", title: "Standard", blurb: "Values up to 4 KB." },
  { tier: "Advanced", title: "Advanced", blurb: "Values up to 8 KB. Can't be changed back to Standard." },
  { tier: "Intelligent-Tiering", title: "Intelligent-Tiering", blurb: "Standard when the value fits, Advanced otherwise." },
]

export const DATA_TYPES: { type: SsmDataType; blurb: string }[] = [
  { type: "text", blurb: "Any value." },
  { type: "aws:ec2:image", blurb: "Validated as an AMI ID (ami-...)." },
  { type: "aws:ssm:integration", blurb: "Integration credentials; SecureString only." },
]

/** maxForTier is the largest value the chosen tier accepts. */
export const maxForTier = (tier: string) => (tier === "Standard" ? MAX_STANDARD : MAX_VALUE)

/** Tag accent per parameter tier. */
export const TIER_ACCENT: Record<SsmParameterTier, TagAccent> = { Standard: "neutral", Advanced: "info", "Intelligent-Tiering": "violet" }

export function TierBadge({ tier }: { tier?: string }) {
  const t = tier || "Standard"
  return (
    <Tag mono={false} accent={TIER_ACCENT[t as SsmParameterTier] ?? "neutral"}>
      {t}
    </Tag>
  )
}
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

/** Tag accent per parameter type. */
export const TYPE_ACCENT: Record<SsmParameterType, TagAccent> = { String: "neutral", StringList: "info", SecureString: "warning" }

export function TypeBadge({ type }: { type: SsmParameterType | string }) {
  return <Tag accent={TYPE_ACCENT[type as SsmParameterType] ?? "neutral"}>{type}</Tag>
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
