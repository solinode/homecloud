"use client"

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react"
import { useRouter } from "next/navigation"
import { useTheme } from "next-themes"
import { Command as Cmdk } from "cmdk"
import * as DialogPrimitive from "@radix-ui/react-dialog"
import { BookOpen, Clock, CornerDownLeft, LogOut, Monitor, Moon, Plus, Search, Sun } from "lucide-react"

import { ServiceIcon } from "@/components/console/service-icon"
import { DEMO, logout } from "@/lib/api"
import { DOCS_URL, QUICK_ACTIONS } from "@/lib/actions"
import { useRecentResources } from "@/lib/recent"
import { SERVICES, serviceForPath } from "@/lib/services"
import { cn } from "@/lib/utils"

interface PaletteCtx {
  open: boolean
  setOpen: (o: boolean) => void
}
const Ctx = createContext<PaletteCtx>({ open: false, setOpen: () => {} })
export const useCommandPalette = () => useContext(Ctx)

/** isTypingTarget is true for inputs, editors and the instance terminal. */
function isTypingTarget(el: EventTarget | null) {
  const e = el as HTMLElement | null
  if (!e) return false
  return ["INPUT", "TEXTAREA", "SELECT"].includes(e.tagName) || e.isContentEditable || !!e.closest?.(".xterm, .cm-editor, .monaco-editor")
}

/** CommandPaletteProvider owns the palette and its shortcuts: ⌘K / Ctrl+K anywhere, "/" outside inputs. */
export function CommandPaletteProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        if ((e.target as HTMLElement | null)?.closest?.(".xterm")) return
        e.preventDefault()
        setOpen((o) => !o)
      } else if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey && !isTypingTarget(e.target)) {
        e.preventDefault()
        setOpen(true)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  return (
    <Ctx.Provider value={{ open, setOpen }}>
      {children}
      <CommandPalette open={open} onOpenChange={setOpen} />
    </Ctx.Provider>
  )
}

const itemCls =
  "group/item flex h-10 cursor-default items-center gap-3 rounded-md px-2.5 text-sm outline-none select-none data-[selected=true]:bg-accent data-[disabled=true]:opacity-50"
const groupCls =
  "px-2 pb-2 [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pt-3 [&_[cmdk-group-heading]]:pb-1.5 [&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:tracking-[0.08em] [&_[cmdk-group-heading]]:text-faint [&_[cmdk-group-heading]]:uppercase"

/**
 * matchScore ranks by words rather than cmdk's scattered-letter fuzzy match:
 * every search word must start a word in the item (so "buck" finds S3 via
 * "Buckets", not Lambda). Items whose name starts with the query rank first.
 */
function matchScore(value: string, search: string): number {
  const words = search.toLowerCase().trim().split(/\s+/).filter(Boolean)
  if (!words.length) return 1
  const v = value.toLowerCase()
  const tokens = v.split(/[\s/:.,()·-]+/)
  if (!words.every((w) => tokens.some((t) => t.startsWith(w)) || (w.length > 2 && v.includes(w)))) return 0
  // value is "<kind> <name> ...": prefer a match on the name itself
  const name = v.split(" ").slice(1, 3).join(" ")
  return name.startsWith(words.join(" ")) ? 1 : name.includes(words[0]) ? 0.8 : 0.5
}

function Enter() {
  return <CornerDownLeft className="text-faint ml-auto size-3.5 opacity-0 group-data-[selected=true]/item:opacity-100" />
}

function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const { setTheme } = useTheme()
  const recent = useRecentResources()
  const [q, setQ] = useState("")

  useEffect(() => {
    if (!open) setQ("")
  }, [open])

  const pages = useMemo(
    () =>
      SERVICES.filter((s) => !s.comingSoon).flatMap((s) =>
        (s.nav ?? []).flatMap((sec) =>
          sec.items
            .filter((it) => it.href !== s.href && serviceForPath(it.href)?.id === s.id)
            .map((it) => ({ service: s, label: it.label, href: it.href })),
        ),
      ),
    [],
  )

  const run = useCallback(
    (fn: () => void) => {
      onOpenChange(false)
      fn()
    },
    [onOpenChange],
  )
  const go = (href: string) => run(() => router.push(href))

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-black/50 backdrop-blur-[2px]" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="bg-popover data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=open]:zoom-in-[0.98] fixed top-[12vh] left-1/2 z-50 w-[calc(100%-1.5rem)] max-w-[640px] -translate-x-1/2 overflow-hidden rounded-xl border border-border-strong shadow-lg duration-150"
        >
          <DialogPrimitive.Title className="sr-only">Search HomeCloud</DialogPrimitive.Title>
          <Cmdk loop filter={matchScore} className="flex flex-col" label="Search services, pages, resources and actions">
            <div className="flex h-14 items-center gap-3 border-b px-4">
              <Search className="text-muted-foreground size-4 shrink-0" />
              <Cmdk.Input
                value={q}
                onValueChange={setQ}
                placeholder="Search services, pages, recent resources, actions…"
                className="placeholder:text-faint h-full flex-1 bg-transparent text-[15px] outline-none"
              />
              <kbd className="text-faint hidden rounded border px-1.5 py-0.5 font-mono text-[10px] sm:inline">ESC</kbd>
            </div>
            <Cmdk.List className="max-h-[min(60vh,440px)] scroll-py-2 overflow-y-auto overscroll-contain py-1">
              <Cmdk.Empty className="text-muted-foreground py-10 text-center text-sm">No results for “{q}”</Cmdk.Empty>
              {recent.length > 0 && (
                <Cmdk.Group heading="Recent" className={groupCls}>
                  {recent.slice(0, q ? 8 : 5).map((r) => (
                    <Cmdk.Item key={r.href} value={`recent ${r.label} ${r.kind} ${r.service}`} onSelect={() => go(r.href)} className={itemCls}>
                      <Clock className="text-faint size-4" />
                      <span className="min-w-0 truncate font-mono text-[13px]">{r.label}</span>
                      <span className="text-faint truncate text-xs">
                        {r.service} · {r.kind}
                      </span>
                      <Enter />
                    </Cmdk.Item>
                  ))}
                </Cmdk.Group>
              )}
              <Cmdk.Group heading="Services" className={groupCls}>
                {SERVICES.filter((s) => !s.comingSoon).map((s) => (
                  <Cmdk.Item key={s.id} value={`service ${s.name} ${s.short} ${s.description} ${s.category} ${(s.nav ?? []).flatMap((n) => n.items.map((i) => i.label)).join(" ")}`} onSelect={() => go(s.href)} className={itemCls}>
                    <ServiceIcon service={s} size="sm" />
                    <span className="font-medium">{s.name}</span>
                    <span className="text-faint hidden truncate text-xs sm:inline">{s.description}</span>
                    <Enter />
                  </Cmdk.Item>
                ))}
              </Cmdk.Group>
              {q && (
                <Cmdk.Group heading="Pages" className={groupCls}>
                  {pages.map((p) => (
                    <Cmdk.Item key={p.href + p.label} value={`page ${p.service.name} ${p.label}`} onSelect={() => go(p.href)} className={itemCls}>
                      <ServiceIcon service={p.service} size="sm" />
                      <span className="text-muted-foreground">{p.service.name}</span>
                      <span className="text-faint">/</span>
                      <span>{p.label}</span>
                      <Enter />
                    </Cmdk.Item>
                  ))}
                </Cmdk.Group>
              )}
              <Cmdk.Group heading="Actions" className={groupCls}>
                {QUICK_ACTIONS.map((a) => (
                  <Cmdk.Item key={a.href} value={`action ${a.label}`} onSelect={() => go(a.href)} className={itemCls}>
                    <span className="bg-brand-soft text-primary border-brand-line flex size-6 items-center justify-center rounded-md border">
                      <Plus className="size-3.5" />
                    </span>
                    <span>{a.label}</span>
                    <Enter />
                  </Cmdk.Item>
                ))}
              </Cmdk.Group>
              <Cmdk.Group heading="Preferences" className={groupCls}>
                <Cmdk.Item value="theme dark mode" onSelect={() => run(() => setTheme("dark"))} className={itemCls}>
                  <Moon className="text-muted-foreground size-4" /> Dark theme
                </Cmdk.Item>
                <Cmdk.Item value="theme light mode" onSelect={() => run(() => setTheme("light"))} className={itemCls}>
                  <Sun className="text-muted-foreground size-4" /> Light theme
                </Cmdk.Item>
                <Cmdk.Item value="theme system" onSelect={() => run(() => setTheme("system"))} className={itemCls}>
                  <Monitor className="text-muted-foreground size-4" /> Match system theme
                </Cmdk.Item>
                <Cmdk.Item value="documentation docs help" onSelect={() => run(() => window.open(DOCS_URL, "_blank", "noopener"))} className={itemCls}>
                  <BookOpen className="text-muted-foreground size-4" /> Documentation
                </Cmdk.Item>
                <Cmdk.Item
                  value="sign out log out"
                  onSelect={() =>
                    run(async () => {
                      await logout()
                      window.location.href = DEMO ? "/" : "/login/"
                    })
                  }
                  className={itemCls}
                >
                  <LogOut className="text-muted-foreground size-4" /> Sign out
                </Cmdk.Item>
              </Cmdk.Group>
            </Cmdk.List>
            <div className="text-faint flex h-10 items-center gap-4 border-t px-4 text-[11px]">
              <span className="flex items-center gap-1.5">
                <Kbd>↑</Kbd>
                <Kbd>↓</Kbd> navigate
              </span>
              <span className="flex items-center gap-1.5">
                <Kbd>↵</Kbd> open
              </span>
              <span className="ml-auto hidden items-center gap-1.5 sm:flex">
                <Kbd>⌘</Kbd>
                <Kbd>K</Kbd> toggle
              </span>
            </div>
          </Cmdk>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

export function Kbd({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <kbd
      className={cn(
        "bg-muted text-muted-foreground inline-flex h-5 min-w-5 items-center justify-center rounded border px-1 font-mono text-[10px] font-medium",
        className,
      )}
    >
      {children}
    </kbd>
  )
}
