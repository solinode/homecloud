"use client"

import { useEffect } from "react"
import { FlaskConical, RotateCcw } from "lucide-react"
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
      className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 bg-gradient-to-r from-violet-600 via-indigo-600 to-cyan-600 px-3 py-1.5 text-center text-xs text-white"
    >
      <FlaskConical className="size-3.5 shrink-0" aria-hidden />
      <span>
        <strong className="font-semibold">Demo</strong> — sample data, nothing is real.{" "}
        <a href={INSTALL_URL} className="font-semibold underline underline-offset-2 hover:text-white/80">
          Install HomeCloud to run your own cloud
        </a>
      </span>
      <span className="hidden text-white/50 sm:inline">|</span>
      <a href={GITHUB_URL} target="_blank" rel="noreferrer" className="underline underline-offset-2 hover:text-white/80">
        GitHub
      </a>
      <button
        type="button"
        onClick={reset}
        className="inline-flex items-center gap-1 rounded border border-white/40 px-1.5 py-0.5 hover:bg-white/15"
        title="Discard every change you made and restore the sample data"
      >
        <RotateCcw className="size-3" aria-hidden />
        Reset demo
      </button>
    </div>
  )
}
