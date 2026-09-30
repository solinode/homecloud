"use client"

import { useEffect, useRef, useState } from "react"
import { RefreshCw, TerminalSquare } from "lucide-react"

import { Button } from "@/components/ui/button"
import { EmptyState } from "@/components/console/empty-state"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { DEMO, seg, wsUrl } from "@/lib/api"
import type { Instance } from "@/lib/types"
import { INSTANCES_PATH } from "./instance-actions"

type ConnState = "connecting" | "connected" | "closed"

const STATE_BADGE: Record<ConnState, { status: string; label: string }> = {
  connecting: { status: "pending", label: "Connecting" },
  connected: { status: "running", label: "Connected" },
  closed: { status: "stopped", label: "Disconnected" },
}

/**
 * InstanceTerminal is a browser shell (EC2 Instance Connect): an xterm.js
 * terminal bridged to the instance over a WebSocket.
 */
export function InstanceTerminal({ instance }: { instance: Instance }) {
  const running = instance.state === "running"
  const hostRef = useRef<HTMLDivElement>(null)
  const [conn, setConn] = useState<ConnState>("connecting")
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    if (!running || !hostRef.current) return
    const host = hostRef.current
    let disposed = false
    let cleanup = () => {}
    setConn("connecting")

    ;(async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")])
      if (disposed) return
      const geist = getComputedStyle(document.documentElement).getPropertyValue("--font-geist-mono").trim()
      const term = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily: `${geist ? `${geist}, ` : ""}ui-monospace, SFMono-Regular, Menlo, monospace`,
        scrollback: 5000,
        theme: {
          background: "#09090b",
          foreground: "#e4e4e7",
          cursor: "#e4e4e7",
          selectionBackground: "#3f3f46",
        },
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      host.innerHTML = ""
      term.open(host)
      try {
        fit.fit()
      } catch {
        // the container may not be laid out yet
      }
      term.write("\x1b[90mConnecting to " + instance.id + "...\x1b[0m\r\n")

      if (DEMO) {
        term.write("\r\n\x1b[33mThe browser terminal is not available in the demo.\x1b[0m\r\n")
        term.write("Install HomeCloud to open a real shell on your instances.\r\n")
        cleanup = () => term.dispose()
        return
      }

      const ws = new WebSocket(wsUrl(`${INSTANCES_PATH}/${seg(instance.id)}/terminal`))
      const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m))
      const sendSize = () => send({ t: "r", c: term.cols, r: term.rows })

      ws.onopen = () => {
        if (disposed) return
        setConn("connected")
        sendSize()
        term.focus()
      }
      ws.onmessage = (ev) => {
        if (typeof ev.data === "string") term.write(ev.data)
        else if (ev.data instanceof Blob) ev.data.text().then((t) => term.write(t))
      }
      ws.onclose = () => {
        if (disposed) return
        setConn("closed")
        term.write("\r\n\x1b[90m[disconnected]\x1b[0m\r\n")
      }
      ws.onerror = () => {
        if (!disposed) term.write("\r\n\x1b[31mCould not connect to the instance terminal.\x1b[0m\r\n")
      }

      const onData = term.onData((d) => send({ t: "i", d }))
      const onResize = term.onResize(() => sendSize())
      const ro = new ResizeObserver(() => {
        try {
          fit.fit()
        } catch {
          // ignore transient layout errors
        }
      })
      ro.observe(host)

      cleanup = () => {
        ro.disconnect()
        onData.dispose()
        onResize.dispose()
        ws.onclose = null
        ws.close()
        term.dispose()
      }
    })()

    return () => {
      disposed = true
      cleanup()
    }
  }, [running, instance.id, attempt])

  if (!running) {
    return (
      <Section title="Connect">
        <EmptyState
          icon={TerminalSquare}
          title="The instance is not running"
          description={
            <>
              The browser terminal opens a shell inside a running instance. This instance is <StatusBadge status={instance.state} />
              {instance.state === "stopped" ? "; start it to connect." : "."}
            </>
          }
        />
      </Section>
    )
  }

  const badge = STATE_BADGE[conn]
  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          Browser terminal <StatusBadge status={badge.status} label={badge.label} />
        </span>
      }
      description="An interactive shell (bash if available, else sh) inside the instance, like EC2 Instance Connect."
      actions={
        <Button variant="outline" size="sm" onClick={() => setAttempt((a) => a + 1)} disabled={conn === "connecting"}>
          <RefreshCw /> {conn === "closed" ? "Reconnect" : "Restart session"}
        </Button>
      }
    >
      <div className="overflow-hidden rounded-md border border-zinc-800 bg-[#09090b]">
        <div ref={hostRef} className="hc-terminal h-[520px] w-full" />
      </div>
    </Section>
  )
}
