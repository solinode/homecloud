"use client"

import { Suspense, useEffect, useState } from "react"
import { useSearchParams } from "next/navigation"
import { AlertCircle, Loader2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Logo } from "@/components/console/topbar"
import { BASE_PATH, errorMessage, getSession, login } from "@/lib/api"

function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.startsWith("/login")) return "/"
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
        <div role="alert" className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-md border p-3 text-sm">
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
      <Button type="submit" disabled={pending || !username || !password} className="mt-1 w-full">
        {pending && <Loader2 className="animate-spin" />}
        Sign in
      </Button>
    </form>
  )
}

export default function LoginPage() {
  return (
    <div className="flex min-h-screen flex-col">
      <header className="bg-topbar text-topbar-foreground flex h-12 items-center px-4">
        <Logo />
      </header>
      <main className="flex flex-1 items-center justify-center p-4">
        <div className="w-full max-w-sm">
          <div className="bg-card rounded-xl border p-6 shadow-sm sm:p-8">
            <h1 className="text-xl font-semibold tracking-tight">Sign in</h1>
            <p className="text-muted-foreground mt-1 mb-6 text-sm">Sign in to the HomeCloud console with your IAM user name and password.</p>
            <Suspense fallback={null}>
              <LoginForm />
            </Suspense>
          </div>
          <p className="text-muted-foreground mt-4 text-center text-xs">
            Forgot the root password? Run <code className="bg-muted rounded px-1 py-0.5">homecloud serve --reset-root-password</code>
          </p>
        </div>
      </main>
    </div>
  )
}
