"use client"

import { useEffect } from "react"
import { ArrowRight, RotateCcw } from "lucide-react"
import { toast } from "sonner"

import { DEMO, DEMO_UNAVAILABLE_HREF, loadDemo } from "@/lib/api"

const INSTALL_URL = "https://homecloud.pages.dev/#start"
const GITHUB_URL = "https://github.com/solinode/homecloud"

/** DemoBanner is rendered above the top bar of every page in the static demo build. */
export function DemoBanner() {
  useEffect(() => {
    if (!DEMO) return
    // Download links cannot work without a server: explain instead of 404ing.
    const onClick = (e: MouseEvent) => {
      const a = (e.target as Element | null)?.closest?.("a")
      if (a && a.getAttribute("href") === DEMO_UNAVAILABLE_HREF) {
        e.preventDefault()
        toast.info("Downloads are not available in the demo. Install HomeCloud to use them on your own machine.")
      }
    }
    document.addEventListener("click", onClick, true)
    return () => document.removeEventListener("click", onClick, true)
  }, [])

  const reset = async () => {
    const demo = await loadDemo()
    demo.demoReset()
    window.location.reload()
  }

  if (!DEMO) return null
  return (
    <div
      role="note"
      className="bg-brand-soft border-brand-line/60 text-muted-foreground relative z-50 flex flex-wrap items-center justify-center gap-x-3 gap-y-1 border-b px-3 py-1.5 text-center text-xs"
    >
      <span className="bg-brand-soft text-primary border-brand-line rounded-full border px-2 py-px font-mono text-[10px] font-semibold tracking-[0.08em] uppercase">
        Demo
      </span>
      <span>Sample data in your browser, nothing is real.</span>
      <a href={INSTALL_URL} className="text-foreground group inline-flex items-center gap-1 font-medium hover:underline hover:underline-offset-2">
        Install HomeCloud <ArrowRight className="size-3 transition-transform group-hover:translate-x-0.5" aria-hidden />
      </a>
      <span className="text-faint hidden sm:inline" aria-hidden>
        ·
      </span>
      <a href={GITHUB_URL} target="_blank" rel="noreferrer" className="hover:text-foreground hidden sm:inline">
        GitHub
      </a>
      <button
        type="button"
        onClick={reset}
        className="hover:text-foreground hover:bg-background/40 inline-flex items-center gap-1 rounded border border-transparent px-1.5 py-0.5 hover:border-border"
        title="Discard every change you made and restore the sample data"
      >
        <RotateCcw className="size-3" aria-hidden />
        Reset demo
      </button>
    </div>
  )
}
