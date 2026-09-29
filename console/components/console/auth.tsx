"use client"

import { createContext, useContext, useEffect, useState, type ReactNode } from "react"

import { getSession, onSessionChange, type Session } from "@/lib/api"

interface AuthState {
  session: Session | null
  ready: boolean
}

const AuthContext = createContext<AuthState>({ session: null, ready: false })

/** AuthGate renders children only with a valid session; otherwise it redirects to /login. */
export function AuthGate({ children, fallback }: { children: ReactNode; fallback?: ReactNode }) {
  const [state, setState] = useState<AuthState>({ session: null, ready: false })

  useEffect(() => {
    const s = getSession()
    setState({ session: s, ready: true })
    if (!s) {
      const next = window.location.pathname + window.location.search
      window.location.replace(`/login/?next=${encodeURIComponent(next)}`)
    }
    return onSessionChange((ns) => setState({ session: ns, ready: true }))
  }, [])

  if (!state.ready || !state.session) return <>{fallback ?? null}</>
  return <AuthContext.Provider value={state}>{children}</AuthContext.Provider>
}

/** useSession returns the signed-in session (non-null inside AuthGate). */
export function useSession(): Session {
  const { session } = useContext(AuthContext)
  if (!session) {
    return { token: "", expires: "", account_id: "", user: { name: "", id: "", arn: "", root: false } }
  }
  return session
}
