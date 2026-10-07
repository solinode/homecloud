"use client"

import { Suspense, useEffect, useState } from "react"
import { useSearchParams } from "next/navigation"
import { useTheme } from "next-themes"
import { AlertCircle, Loader2, Moon, Sun } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { BrandMark, Logo } from "@/components/console/brand"
import { BASE_PATH, errorMessage, getSession, login } from "@/lib/api"

// safeNext only lets a same-site path through. Browsers read "\" as "/" and drop
// tabs and newlines inside URLs, so "/\evil.example" and "/<tab>/evil.example"
// both mean "//evil.example": refuse them by character, then confirm by resolving
// the path against this origin.
function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.startsWith("/login")) return "/"
  // eslint-disable-next-line no-control-regex
  if (/[\\\u0000-\u001f\u007f]/.test(next)) return "/"
  if (typeof window !== "undefined") {
    try {
      if (new URL(next, window.location.origin).origin !== window.location.origin) return "/"
    } catch {
      return "/"
    }
  }
  return next
}

function LoginForm() {
  const sp = useSearchParams()
  const next = safeNext(sp.get("next"))
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (getSession()) window.location.replace(BASE_PATH + next)
  }, [next])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await login(username.trim(), password)
      window.location.replace(BASE_PATH + next)
    } catch (err) {
      setError(errorMessage(err))
      setPending(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      {error && (
        <div role="alert" className="border-danger/25 bg-danger-soft text-danger flex items-start gap-2 rounded-lg border p-3 text-sm">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <span>{error}</span>
        </div>
      )}
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="username">User name</Label>
        <Input id="username" autoComplete="username" autoFocus required value={username} onChange={(e) => setUsername(e.target.value)} placeholder="root" />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="password">Password</Label>
        <Input id="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
      </div>
      <Button type="submit" disabled={pending || !username || !password} size="lg" className="mt-2 w-full">
        {pending && <Loader2 className="animate-spin" />}
        Sign in
      </Button>
    </form>
  )
}

export default function LoginPage() {
  return (
    <div className="hc-backdrop flex min-h-dvh flex-col">
      <header className="flex h-16 items-center justify-between px-5 sm:px-8">
        <Logo />
        <ThemeButton />
      </header>
      <main className="flex flex-1 items-center justify-center px-4 pt-4 pb-16">
        <div className="w-full max-w-[400px]">
          <div className="mb-8 flex flex-col items-center text-center">
            <BrandMark className="size-12 rounded-[13px] shadow-[inset_0_0_0_1px_rgb(255_255_255/0.1),0_16px_40px_-10px_var(--glow)]" />
            <h1 className="mt-6 text-[26px] leading-tight font-semibold tracking-[-0.04em]">Sign in to HomeCloud</h1>
            <p className="text-muted-foreground mt-2 text-sm">Use your IAM user name and password.</p>
          </div>
          <div className="bg-card rounded-2xl border p-6 shadow-lg sm:p-7">
            <Suspense fallback={null}>
              <LoginForm />
            </Suspense>
          </div>
          <div className="text-faint mt-6 flex flex-col gap-2 text-center text-xs leading-relaxed">
            <p>
              First run? The root password is printed once in the server log.
            </p>
            <p>
              Forgot it? Run{" "}
              <code className="bg-muted text-muted-foreground inline-block rounded border px-1.5 py-0.5 font-mono text-[11px] whitespace-nowrap">homecloud serve --reset-root-password</code>
            </p>
          </div>
        </div>
      </main>
    </div>
  )
}

function ThemeButton() {
  const { resolvedTheme, setTheme } = useTheme()
  const [mounted, setMounted] = useState(false)
  useEffect(() => setMounted(true), [])
  const dark = !mounted || resolvedTheme === "dark"
  return (
    <Button variant="ghost" size="icon-sm" onClick={() => setTheme(dark ? "light" : "dark")} aria-label="Toggle dark mode">
      {dark ? <Sun /> : <Moon />}
    </Button>
  )
}
