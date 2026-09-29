"use client"

import { useEffect, useState } from "react"
import { Loader2, Save } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Section } from "@/components/console/section"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { PasswordPolicy, UpdateUserPoolInput, UserPool } from "@/lib/types"

import { COGNITO_PATH, PasswordPolicyFields, SwitchRow, minLengthError, poolPath } from "./shared"

const samePolicy = (a: PasswordPolicy, b: PasswordPolicy) =>
  a.min_length === b.min_length &&
  a.require_uppercase === b.require_uppercase &&
  a.require_lowercase === b.require_lowercase &&
  a.require_numbers === b.require_numbers &&
  a.require_symbols === b.require_symbols

export function SettingsTab({ pool }: { pool: UserPool }) {
  const [policy, setPolicy] = useState(pool.password_policy)
  const [autoConfirm, setAutoConfirm] = useState(pool.auto_confirm)
  const [selfSignUp, setSelfSignUp] = useState(pool.self_sign_up)
  const [pending, setPending] = useState(false)

  // Re-sync when the saved pool changes (after a save or a refresh).
  useEffect(() => {
    setPolicy(pool.password_policy)
    setAutoConfirm(pool.auto_confirm)
    setSelfSignUp(pool.self_sign_up)
  }, [pool.password_policy, pool.auto_confirm, pool.self_sign_up])

  const dirty = !samePolicy(policy, pool.password_policy) || autoConfirm !== pool.auto_confirm || selfSignUp !== pool.self_sign_up
  const minErr = minLengthError(policy.min_length)

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (minErr) return
    const body: UpdateUserPoolInput = { password_policy: policy, auto_confirm: autoConfirm, self_sign_up: selfSignUp }
    setPending(true)
    try {
      await api.patch(poolPath(pool.id), body)
      toast.success("User pool settings saved")
      await revalidate(COGNITO_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <form onSubmit={save} className="flex flex-col gap-4">
      <Section title="Password policy" description="Applies to new passwords: sign-up, administrator-set passwords and password changes. Existing passwords keep working.">
        <PasswordPolicyFields value={policy} onChange={setPolicy} idPrefix="set-pp" error={minErr} />
      </Section>
      <Section title="Sign-up">
        <div className="flex flex-col gap-2">
          <SwitchRow
            id="set-self"
            label="Self sign-up"
            description="Allow POST /cognito/{pool}/sign-up. When off, only administrators create users."
            checked={selfSignUp}
            onChange={setSelfSignUp}
          />
          <SwitchRow
            id="set-auto"
            label="Auto-confirm new users"
            description="Self-registered users can sign in immediately. When off, they stay unconfirmed until confirmed from the Users tab."
            checked={autoConfirm}
            onChange={setAutoConfirm}
          />
        </div>
      </Section>
      <div className="flex justify-end gap-2">
        <Button
          type="button"
          variant="outline"
          disabled={!dirty || pending}
          onClick={() => {
            setPolicy(pool.password_policy)
            setAutoConfirm(pool.auto_confirm)
            setSelfSignUp(pool.self_sign_up)
          }}
        >
          Reset
        </Button>
        <Button type="submit" disabled={!dirty || pending || !!minErr}>
          {pending ? <Loader2 className="animate-spin" /> : <Save />} Save changes
        </Button>
      </div>
    </form>
  )
}
