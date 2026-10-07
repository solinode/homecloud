"use client"

import { useId, useMemo, useState, type ReactNode } from "react"
import { AlertCircle, Maximize2, Minimize2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import type { SfnHistoryEvent } from "@/lib/types"
import { cn } from "@/lib/utils"

/**
 * StateMachineGraph draws an Amazon States Language definition as an SVG
 * flow chart laid out top to bottom from StartAt (BFS layers). Parallel and
 * Map states render their branches / item processor as nested groups.
 */

export type StateStatus = "entered" | "succeeded" | "failed" | "caught"

const NODE_W = 184
const NODE_H = 46
const H_GAP = 32
const V_GAP = 60
const PAD = 16
const MARK_R = 7
const MARK_GAP = 30
const GROUP_HEAD = 40
const GROUP_PAD = 14
const BRANCH_GAP = 14
const BACK_LANE = 26
const MAX_DEPTH = 8

type Obj = Record<string, unknown>
const isObj = (v: unknown): v is Obj => !!v && typeof v === "object" && !Array.isArray(v)

class GraphError extends Error {}

interface LEdge {
  path: string
  dashed: boolean
  label?: string
  lx: number
  ly: number
}

interface LNode {
  name: string
  type: string
  x: number
  y: number
  w: number
  h: number
  terminal: boolean
  groups?: { block: Block; x: number; y: number }[]
}

interface Block {
  w: number
  h: number
  nodes: LNode[]
  edges: LEdge[]
  start?: { x: number; y: number }
  end?: { x: number; y: number }
  /** Shown instead of nodes when a nested machine cannot be drawn. */
  note?: string
}

interface Succ {
  to: string
  kind: "next" | "choice" | "default" | "catch"
  label?: string
}

// ---- labels ----

const OPS: Record<string, string> = { Equals: "==", LessThan: "<", GreaterThan: ">", LessThanEquals: "<=", GreaterThanEquals: ">=", Matches: "~" }

/** choiceLabel summarizes a Choice rule: "total > 100", "And (2 rules)". */
export function choiceLabel(rule: unknown): string {
  if (!isObj(rule)) return "?"
  if (Array.isArray(rule.And)) return `And (${rule.And.length} rules)`
  if (Array.isArray(rule.Or)) return `Or (${rule.Or.length} rules)`
  if (isObj(rule.Not)) return `Not ${choiceLabel(rule.Not)}`
  const v = typeof rule.Variable === "string" ? rule.Variable.replace(/^\$\.?/, "") || "$" : "?"
  const op = Object.keys(rule).find((k) => k !== "Variable" && k !== "Next" && k !== "Comment")
  if (!op) return v
  const val = rule[op]
  const m = op.match(/^(String|Numeric|Boolean|Timestamp)(Equals|LessThan|GreaterThan|LessThanEquals|GreaterThanEquals|Matches)(Path)?$/)
  if (m) return `${v} ${OPS[m[2]]} ${m[3] ? String(val).replace(/^\$\.?/, "") : JSON.stringify(val)}`
  return `${v} ${op}${val === true ? "" : `=${JSON.stringify(val)}`}`
}

const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, Math.max(1, n - 1))}…` : s)

// ---- layout ----

function successors(states: Obj, name: string): Succ[] {
  const s = states[name]
  if (!isObj(s)) return []
  const out: Succ[] = []
  const add = (to: unknown, kind: Succ["kind"], label?: string) => {
    if (typeof to === "string" && to in states) out.push({ to, kind, label })
  }
  add(s.Next, "next")
  if (Array.isArray(s.Choices)) for (const c of s.Choices) if (isObj(c)) add(c.Next, "choice", choiceLabel(c))
  add(s.Default, "default", "Default")
  if (Array.isArray(s.Catch)) {
    for (const c of s.Catch) {
      if (!isObj(c)) continue
      const errs = Array.isArray(c.ErrorEquals) ? c.ErrorEquals.map(String).join(", ") : "error"
      add(c.Next, "catch", `Catch: ${errs}`)
    }
  }
  return out
}

function noteBlock(note: string): Block {
  return { w: NODE_W + 2 * PAD, h: 56, nodes: [], edges: [], note }
}

function nodeFor(name: string, st: unknown, depth: number): LNode {
  const s = isObj(st) ? st : {}
  const type = typeof s.Type === "string" ? s.Type : "?"
  const terminal = type === "Succeed" || type === "Fail" || s.End === true
  const node: LNode = { name, type, x: 0, y: 0, w: NODE_W, h: NODE_H, terminal }
  let subs: unknown[] | null = null
  if (type === "Parallel") subs = Array.isArray(s.Branches) ? s.Branches : []
  else if (type === "Map") subs = s.ItemProcessor ?? s.Iterator ? [s.ItemProcessor ?? s.Iterator] : []
  if (!subs) return node
  const blocks = subs.length
    ? subs.map((b) => {
        if (depth >= MAX_DEPTH) return noteBlock("Nested too deeply to draw")
        try {
          return layoutBlock(b, depth + 1)
        } catch (e) {
          return noteBlock(e instanceof Error ? e.message : String(e))
        }
      })
    : [noteBlock(type === "Map" ? "No ItemProcessor" : "No Branches")]
  const innerW = blocks.reduce((a, b) => a + b.w, 0) + (blocks.length - 1) * BRANCH_GAP
  const innerH = Math.max(...blocks.map((b) => b.h))
  node.w = Math.max(NODE_W, innerW + 2 * GROUP_PAD)
  node.h = GROUP_HEAD + innerH + GROUP_PAD
  let x = (node.w - innerW) / 2
  node.groups = blocks.map((block) => {
    const g = { block, x, y: GROUP_HEAD }
    x += block.w + BRANCH_GAP
    return g
  })
  return node
}

/** Point on the cubic Bezier used for forward edges. */
function bez(t: number, a: number, b: number, c: number, d: number) {
  const u = 1 - t
  return u * u * u * a + 3 * u * u * t * b + 3 * u * t * t * c + t * t * t * d
}

function layoutBlock(m: unknown, depth = 0): Block {
  if (!isObj(m)) throw new GraphError("The definition must be a JSON object")
  const states = m.States
  if (!isObj(states) || Object.keys(states).length === 0) throw new GraphError("The definition has no States")
  const names = Object.keys(states)
  const startAt = typeof m.StartAt === "string" && m.StartAt in states ? m.StartAt : null

  // Layers from StartAt: BFS gives the order within a layer; loops (DFS back
  // edges) are ignored and each state sits one layer below its deepest
  // predecessor, so joins are drawn under every branch that leads to them.
  const layerOf = new Map<string, number>()
  const order: string[] = []
  if (startAt) {
    const seen = new Set([startAt])
    const queue = [startAt]
    while (queue.length) {
      const n = queue.shift()!
      order.push(n)
      for (const e of successors(states, n)) {
        if (!seen.has(e.to)) {
          seen.add(e.to)
          queue.push(e.to)
        }
      }
    }
    const back = new Set<string>()
    const onStack = new Set<string>()
    const done = new Set<string>()
    const dfs = (n: string) => {
      onStack.add(n)
      for (const e of successors(states, n)) {
        if (onStack.has(e.to)) back.add(`${n}\u0000${e.to}`)
        else if (!done.has(e.to)) dfs(e.to)
      }
      onStack.delete(n)
      done.add(n)
    }
    dfs(startAt)
    // Longest path over the remaining DAG (Kahn's algorithm).
    const indeg = new Map(order.map((n) => [n, 0]))
    const fwd = (n: string) => successors(states, n).filter((e) => !back.has(`${n}\u0000${e.to}`))
    for (const n of order) for (const e of fwd(n)) indeg.set(e.to, indeg.get(e.to)! + 1)
    for (const n of order) layerOf.set(n, 0)
    const ready = order.filter((n) => indeg.get(n) === 0)
    while (ready.length) {
      const n = ready.shift()!
      for (const e of fwd(n)) {
        layerOf.set(e.to, Math.max(layerOf.get(e.to)!, layerOf.get(n)! + 1))
        indeg.set(e.to, indeg.get(e.to)! - 1)
        if (indeg.get(e.to) === 0) ready.push(e.to)
      }
    }
  }
  const lastLayer = order.length ? Math.max(...layerOf.values()) : -1
  for (const n of names) {
    if (!layerOf.has(n)) {
      layerOf.set(n, lastLayer + 1)
      order.push(n)
    }
  }

  const nodes = new Map<string, LNode>()
  for (const n of order) nodes.set(n, nodeFor(n, states[n], depth))
  const layers: LNode[][] = []
  for (const n of order) (layers[layerOf.get(n)!] ??= []).push(nodes.get(n)!)

  const layerW = layers.map((l) => l.reduce((a, n) => a + n.w, 0) + (l.length - 1) * H_GAP)
  const contentW = Math.max(...layerW, NODE_W)
  const cx = PAD + contentW / 2

  let y = PAD + (startAt ? 2 * MARK_R + MARK_GAP : 0)
  layers.forEach((l, i) => {
    let x = PAD + (contentW - layerW[i]) / 2
    const lh = Math.max(...l.map((n) => n.h))
    for (const n of l) {
      n.x = x
      n.y = y
      x += n.w + H_GAP
    }
    y += lh + V_GAP
  })
  const bottom = y - V_GAP
  const hasTerminal = [...nodes.values()].some((n) => n.terminal)
  const start = startAt ? { x: cx, y: PAD + MARK_R } : undefined
  const end = hasTerminal ? { x: cx, y: bottom + MARK_GAP + MARK_R } : undefined

  // ---- edges ----
  const edges: LEdge[] = []
  let lanes = 0
  if (start && startAt) {
    const t = nodes.get(startAt)!
    const tx = t.x + t.w / 2
    edges.push({ path: `M ${start.x} ${start.y + MARK_R} C ${start.x} ${t.y - 12}, ${tx} ${start.y + 12}, ${tx} ${t.y}`, dashed: false, lx: 0, ly: 0 })
  }
  for (const n of order) {
    const from = nodes.get(n)!
    // Merge parallel edges to the same target (a choice rule and the Default, say).
    const merged = new Map<string, { to: string; dashed: boolean; labels: string[] }>()
    for (const e of successors(states, n)) {
      const key = `${e.to}|${e.kind === "catch"}`
      const cur = merged.get(key) ?? { to: e.to, dashed: e.kind === "catch", labels: [] }
      if (e.label) cur.labels.push(e.label)
      merged.set(key, cur)
    }
    const list = [...merged.values()]
    // Forward edges go straight down unless a state in a layer in between is in the way.
    const blocked = (e: { to: string }) => {
      const to = nodes.get(e.to)!
      const a = Math.min(from.x, to.x + to.w / 2) - 6
      const b = Math.max(from.x + from.w, to.x + to.w / 2) + 6
      const la = layerOf.get(n)!
      const lb = layerOf.get(e.to)!
      return [...nodes.values()].some((m) => m !== from && m !== to && layerOf.get(m.name)! > la && layerOf.get(m.name)! < lb && m.x < b && m.x + m.w > a)
    }
    const forward = list.filter((e) => layerOf.get(e.to)! > layerOf.get(n)! && !blocked(e))
    const outCount = forward.length + (from.terminal && end ? 1 : 0)
    let slot = 0
    const sxAt = () => from.x + (from.w * ++slot) / (outCount + 1)
    for (const e of list) {
      const to = nodes.get(e.to)!
      const label = e.labels.length ? clip(e.labels.join(" / "), 30) : undefined
      if (forward.includes(e)) {
        const sx = sxAt()
        const sy = from.y + from.h
        const tx = to.x + to.w / 2
        const ty = to.y
        const my = (sy + ty) / 2
        const t = label ? 0.42 : 0.5
        edges.push({
          path: `M ${sx} ${sy} C ${sx} ${my}, ${tx} ${my}, ${tx} ${ty}`,
          dashed: e.dashed,
          label,
          lx: bez(t, sx, sx, tx, tx),
          ly: bez(t, sy, my, my, ty),
        })
      } else {
        // Loops, same-layer and obstructed edges run along the right-hand side in their own lane.
        const lane = PAD + contentW + 14 + lanes * BACK_LANE
        lanes++
        const sx = from.x + from.w
        const sy = from.y + from.h / 2 + (e.dashed ? 8 : 0)
        const tx = to.x + to.w
        const ty = to.y + to.h / 2 - 6
        edges.push({
          path: `M ${sx} ${sy} C ${lane + 10} ${sy}, ${lane + 10} ${ty}, ${tx} ${ty}`,
          dashed: e.dashed,
          label,
          lx: (sx + tx + 6 * (lane + 10)) / 8,
          ly: (sy + ty) / 2,
        })
      }
    }
    if (from.terminal && end) {
      const sx = sxAt()
      const sy = from.y + from.h
      const my = (sy + end.y) / 2
      edges.push({ path: `M ${sx} ${sy} C ${sx} ${my}, ${end.x} ${my}, ${end.x} ${end.y - MARK_R}`, dashed: false, lx: 0, ly: 0 })
    }
  }

  const w = PAD * 2 + contentW + (lanes ? 14 + lanes * BACK_LANE + 70 : 0)
  const h = (end ? end.y + MARK_R : bottom) + PAD
  return { w, h, nodes: [...nodes.values()], edges, start, end }
}

function parseDefinition(def: unknown): unknown {
  if (typeof def !== "string") return def
  try {
    return JSON.parse(def)
  } catch {
    throw new GraphError("The definition is not valid JSON")
  }
}

// ---- statuses from execution history ----

/**
 * statusesFromHistory derives per-state statuses from an execution's events:
 * <Type>StateEntered -> entered, <Type>StateExited -> succeeded (or caught when
 * the exit carries a caught error), <Type>StateFailed -> failed. States still
 * "entered" when the execution has stopped are shown as failed.
 */
export function statusesFromHistory(history: SfnHistoryEvent[] | undefined, running: boolean): Record<string, StateStatus> {
  const acc = new Map<string, { entered: number; exited: number; failed: number; lastCaught: boolean }>()
  for (const e of history ?? []) {
    if (!e.state) continue
    const a = acc.get(e.state) ?? { entered: 0, exited: 0, failed: 0, lastCaught: false }
    if (e.type.endsWith("StateEntered")) a.entered++
    else if (e.type.endsWith("StateExited")) {
      a.exited++
      a.lastCaught = isObj(e.details) && "caught" in e.details
    } else if (e.type.endsWith("StateFailed")) a.failed++
    acc.set(e.state, a)
  }
  const out: Record<string, StateStatus> = {}
  for (const [name, a] of acc) {
    if (a.entered > a.exited + a.failed) out[name] = running ? "entered" : "failed"
    else if (a.failed > 0) out[name] = "failed"
    else if (a.lastCaught) out[name] = "caught"
    else if (a.exited > 0) out[name] = "succeeded"
  }
  return out
}

// ---- rendering ----

/** Node outline and fill per state, from the semantic tokens (running = info, caught = warning). */
const STATUS_CLASS: Record<StateStatus, string> = {
  entered: "stroke-info fill-info-soft",
  succeeded: "stroke-success fill-success-soft",
  failed: "stroke-danger fill-danger-soft",
  caught: "stroke-warning fill-warning-soft",
}

const LEGEND: { status: StateStatus; label: string; swatch: string }[] = [
  { status: "entered", label: "In progress", swatch: "border-info bg-info-soft" },
  { status: "succeeded", label: "Succeeded", swatch: "border-success bg-success-soft" },
  { status: "failed", label: "Failed", swatch: "border-danger bg-danger-soft" },
  { status: "caught", label: "Caught error", swatch: "border-dashed border-warning bg-warning-soft" },
]

function EdgeLabel({ x, y, text }: { x: number; y: number; text: string }) {
  const w = text.length * 5.7 + 10
  return (
    <g>
      <rect x={x - w / 2} y={y - 8} width={w} height={16} rx={4} fill="var(--card)" stroke="var(--border)" />
      <text x={x} y={y + 3.5} textAnchor="middle" fontSize={10} fill="var(--muted-foreground)">
        {text}
      </text>
    </g>
  )
}

function NodeView({ node, statuses, arrow }: { node: LNode; statuses?: Record<string, StateStatus>; arrow: string }) {
  const st = statuses?.[node.name]
  const { x, y, w, h } = node
  const title = <title>{`${node.name} (${node.type})${st ? ` - ${LEGEND.find((l) => l.status === st)?.label}` : ""}`}</title>
  const statusCls = st ? STATUS_CLASS[st] : undefined
  const dash = st === "caught" ? "5 3" : undefined

  if (node.groups) {
    return (
      <g>
        {title}
        <rect
          x={x}
          y={y}
          width={w}
          height={h}
          rx={10}
          fill="var(--muted)"
          fillOpacity={st ? undefined : 0.45}
          stroke="var(--border)"
          strokeWidth={st ? 2 : 1.25}
          strokeDasharray={dash}
          className={cn(statusCls, st === "entered" && "animate-pulse")}
        />
        <text x={x + 12} y={y + 18} fontSize={12.5} fontWeight={600} fill="var(--foreground)">
          {clip(node.name, Math.floor((w - 24) / 7.2))}
        </text>
        <text x={x + 12} y={y + 32} fontSize={10.5} fill="var(--muted-foreground)">
          {node.type === "Map" ? "Map · item processor" : `Parallel · ${node.groups.length} branch${node.groups.length === 1 ? "" : "es"}`}
        </text>
        {node.groups.map((g, i) => (
          <g key={i} transform={`translate(${x + g.x} ${y + g.y})`}>
            <rect width={g.block.w} height={g.block.h} rx={8} fill="var(--card)" stroke="var(--border)" strokeDasharray="4 3" />
            <BlockView block={g.block} statuses={statuses} arrow={arrow} />
          </g>
        ))}
      </g>
    )
  }

  const fail = node.type === "Fail"
  return (
    <g>
      {title}
      {st === "entered" && (
        <rect x={x - 4} y={y - 4} width={w + 8} height={h + 8} rx={node.terminal ? (h + 8) / 2 : 12} fill="none" strokeWidth={2} className="stroke-info/60 animate-pulse" />
      )}
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx={node.terminal ? h / 2 : 8}
        fill="var(--card)"
        stroke={fail ? "var(--destructive)" : node.terminal ? "var(--muted-foreground)" : "var(--border)"}
        strokeOpacity={fail && !st ? 0.7 : 1}
        strokeWidth={st ? 2.25 : node.terminal ? 1.75 : 1.25}
        strokeDasharray={dash}
        className={statusCls}
      />
      <text x={x + w / 2} y={y + 20} textAnchor="middle" fontSize={12.5} fontWeight={600} fill="var(--foreground)">
        {clip(node.name, Math.floor((w - 20) / 7.2))}
      </text>
      <text x={x + w / 2} y={y + 35} textAnchor="middle" fontSize={10.5} fill="var(--muted-foreground)">
        {node.type}
        {node.type !== "Succeed" && node.type !== "Fail" && node.terminal ? " · End" : ""}
      </text>
    </g>
  )
}

function BlockView({ block, statuses, arrow }: { block: Block; statuses?: Record<string, StateStatus>; arrow: string }) {
  if (block.note) {
    return (
      <text x={block.w / 2} y={block.h / 2 + 4} textAnchor="middle" fontSize={11} fill="var(--muted-foreground)">
        {clip(block.note, 34)}
      </text>
    )
  }
  return (
    <g>
      {block.edges.map((e, i) => (
        <path
          key={i}
          d={e.path}
          fill="none"
          stroke="var(--muted-foreground)"
          strokeOpacity={0.75}
          strokeWidth={1.4}
          strokeDasharray={e.dashed ? "5 4" : undefined}
          markerEnd={`url(#${arrow})`}
        />
      ))}
      {block.start && <circle cx={block.start.x} cy={block.start.y} r={MARK_R} fill="var(--primary)" />}
      {block.end && (
        <g>
          <circle cx={block.end.x} cy={block.end.y} r={MARK_R} fill="none" stroke="var(--foreground)" strokeWidth={1.5} />
          <circle cx={block.end.x} cy={block.end.y} r={MARK_R - 3} fill="var(--foreground)" />
        </g>
      )}
      {block.nodes.map((n) => (
        <NodeView key={n.name} node={n} statuses={statuses} arrow={arrow} />
      ))}
      {block.edges.map((e, i) => (e.label ? <EdgeLabel key={i} x={e.lx} y={e.ly} text={e.label} /> : null))}
    </g>
  )
}

export function StateMachineGraph({
  definition,
  statuses,
  className,
  toolbar,
}: {
  /** The definition as an object or as JSON text. */
  definition: unknown
  statuses?: Record<string, StateStatus>
  className?: string
  /** Extra controls shown next to the fit toggle. */
  toolbar?: ReactNode
}) {
  const rawId = useId()
  const arrow = `sfn-arrow-${rawId.replace(/[^a-zA-Z0-9_-]/g, "")}`
  const [fit, setFit] = useState(true)
  const result = useMemo(() => {
    try {
      return { block: layoutBlock(parseDefinition(definition)) }
    } catch (e) {
      return { error: e instanceof GraphError ? e.message : `Cannot draw this definition: ${e instanceof Error ? e.message : String(e)}` }
    }
  }, [definition])

  if (!result.block) {
    return (
      <div className={cn("text-muted-foreground flex min-h-40 flex-col items-center justify-center gap-2 rounded-md border border-dashed p-6 text-center text-sm", className)}>
        <AlertCircle className="size-5" />
        <span>{result.error}</span>
      </div>
    )
  }
  const b = result.block
  const hasStatus = statuses && Object.keys(statuses).length > 0
  return (
    <div className={cn("bg-muted/20 flex flex-col rounded-md border", className)}>
      <div className="flex flex-wrap items-center justify-end gap-2 px-2 pt-2">
        {toolbar}
        <Button type="button" variant="ghost" size="sm" className="h-7" onClick={() => setFit(!fit)} title={fit ? "Show at actual size" : "Scale to fit"}>
          {fit ? <Maximize2 /> : <Minimize2 />}
          {fit ? "Actual size" : "Fit"}
        </Button>
      </div>
      <div className="overflow-auto px-2 pb-2">
        <svg
          role="img"
          aria-label="State machine graph"
          viewBox={`0 0 ${b.w} ${b.h}`}
          width={fit ? "100%" : b.w}
          height={fit ? undefined : b.h}
          style={fit ? { maxWidth: b.w, height: "auto" } : { maxWidth: "none" }}
          className="mx-auto block font-sans"
        >
          <defs>
            <marker id={arrow} viewBox="0 0 10 10" refX={9} refY={5} markerWidth={7} markerHeight={7} orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--muted-foreground)" />
            </marker>
          </defs>
          <BlockView block={b} statuses={statuses} arrow={arrow} />
        </svg>
      </div>
      {hasStatus && (
        <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 border-t px-3 py-2 text-xs">
          {LEGEND.map((l) => (
            <span key={l.status} className="flex items-center gap-1.5">
              <span className={cn("inline-block size-3 rounded-sm border-2", l.swatch)} />
              {l.label}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
