"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, Download, FilePlus2, Info, Loader2, Pencil, Rocket, Trash2, Undo2, Upload } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { api, authUrl, errorMessage } from "@/lib/api"
import { formatBytes, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { FunctionCode, LambdaFunction } from "@/lib/types"
import { cn } from "@/lib/utils"

import { CodeEditor } from "./code-editor"
import { LAMBDA_PATH, MAX_ZIP_BYTES, fileToBase64, fnPath, handlerFiles } from "./common"

type Files = Record<string, string>

const sameFiles = (a: Files | null, b: Files) => {
  if (!a) return true
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return false
  return ka.every((k) => k in b && a[k] === b[k])
}

function sortNames(names: string[]) {
  return [...names].sort((a, b) => {
    const da = a.includes("/")
    const db = b.includes("/")
    if (da !== db) return da ? 1 : -1
    return a.localeCompare(b)
  })
}

function fileNameError(name: string, existing: string[], current?: string): string | null {
  const n = name.trim()
  if (!n) return "Enter a file name"
  if (n.startsWith("/") || n.endsWith("/")) return "Use a relative path without a leading or trailing slash, e.g. utils.py or lib/helpers.js"
  if (n.split("/").some((p) => p === ".." || p === "." || p === "")) return "Path segments cannot be empty, . or .."
  if (/[\\\0]/.test(n)) return "Backslashes are not allowed"
  if (n !== current && existing.includes(n)) return "A file with this name already exists"
  return null
}

/** downloadZipUrl is an authenticated link to the function's deployment package. */
export const downloadZipUrl = (name: string) => authUrl(`${fnPath(name)}/code`, { format: "zip" })

export function CodeTab({ fn, active }: { fn: LambdaFunction; active: boolean }) {
  const code = useApi<FunctionCode>(`${fnPath(fn.name)}/code`, { revalidateOnFocus: false })
  const [baseline, setBaseline] = useState<Files | null>(null)
  const [files, setFiles] = useState<Files>({})
  const [loadedSha, setLoadedSha] = useState("")
  const [current, setCurrent] = useState("")
  const [deploying, setDeploying] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [naming, setNaming] = useState<{ mode: "add" | "rename"; from?: string } | null>(null)
  const [deletingFile, setDeletingFile] = useState<string | null>(null)
  const [discarding, setDiscarding] = useState(false)
  const zipInput = useRef<HTMLInputElement>(null)

  const dirty = baseline !== null && !sameFiles(baseline, files)

  // Load the server's code, unless the user has unsaved edits.
  useEffect(() => {
    const d = code.data
    if (!d || d.sha256_hex === loadedSha) return
    if (baseline && !sameFiles(baseline, files)) return
    setBaseline(d.files)
    setFiles(d.files)
    setLoadedSha(d.sha256_hex)
    setCurrent((c) => {
      if (c && c in d.files) return c
      const names = sortNames(Object.keys(d.files))
      const main = handlerFiles(fn.runtime, fn.handler).find((f) => f in d.files)
      return main ?? names[0] ?? ""
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code.data, loadedSha])

  const names = useMemo(() => sortNames(Object.keys(files)), [files])
  const handlerCandidates = handlerFiles(fn.runtime, fn.handler)
  const handlerFound = handlerCandidates.some((f) => f in files)
  const editable = code.data?.editable ?? true

  const deploy = async () => {
    if (!dirty || deploying || !editable) return
    if (names.length === 0) {
      toast.error("Add at least one file before deploying")
      return
    }
    setDeploying(true)
    const snapshot = files
    try {
      await api.put(`${fnPath(fn.name)}/code`, { files: snapshot })
      setBaseline(snapshot)
      toast.success(`Deployed ${pluralize(Object.keys(snapshot).length, "file")} to ${fn.name}`)
      await revalidate(LAMBDA_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setDeploying(false)
    }
  }

  // Ctrl/Cmd+S deploys while the Code tab is visible.
  const deployRef = useRef(deploy)
  deployRef.current = deploy
  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault()
        deployRef.current()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [active])

  // Warn before leaving the page with undeployed changes.
  useEffect(() => {
    if (!dirty) return
    const onBefore = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ""
    }
    window.addEventListener("beforeunload", onBefore)
    return () => window.removeEventListener("beforeunload", onBefore)
  }, [dirty])

  const uploadZip = async (file: File) => {
    if (file.size > MAX_ZIP_BYTES) {
      toast.error(`The package is ${formatBytes(file.size)}; the limit is 50 MB`)
      return
    }
    setUploading(true)
    try {
      const zip_base64 = await fileToBase64(file)
      await api.put(`${fnPath(fn.name)}/code`, { zip_base64 })
      // Drop local edits: the uploaded package replaces everything.
      setBaseline(null)
      setLoadedSha("")
      toast.success(`Uploaded ${file.name} (${formatBytes(file.size)}) to ${fn.name}`)
      await revalidate(LAMBDA_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setUploading(false)
    }
  }

  const header = (
    <>
      <input
        ref={zipInput}
        type="file"
        accept=".zip,application/zip"
        className="hidden"
        onChange={(e) => {
          const f = e.target.files?.[0]
          e.target.value = ""
          if (f) uploadZip(f)
        }}
      />
      <Button variant="outline" size="sm" onClick={() => zipInput.current?.click()} disabled={uploading}>
        {uploading ? <Loader2 className="animate-spin" /> : <Upload />} Upload .zip
      </Button>
      <Button variant="outline" size="sm" asChild>
        <a href={downloadZipUrl(fn.name)} download={`${fn.name}.zip`}>
          <Download /> Download .zip
        </a>
      </Button>
    </>
  )

  if (code.error) {
    return (
      <Section title="Code source" actions={header}>
        <ErrorState error={code.error} onRetry={() => code.mutate()} />
      </Section>
    )
  }
  if (!code.data || baseline === null) {
    return (
      <Section title="Code source" actions={header}>
        <Skeleton className="h-80 rounded-md" />
      </Section>
    )
  }

  if (!editable) {
    return (
      <Section title="Code source" actions={header}>
        <Alert>
          <Info />
          <AlertTitle>The deployment package is too large to edit in the console</AlertTitle>
          <AlertDescription>
            It has {pluralize(code.data.file_count, "file")} ({formatBytes(fn.code_size)} zipped) and contains binary files, files over 256 KB or more than 50
            files. Download the package, change it locally and upload a new .zip.
          </AlertDescription>
        </Alert>
        <p className="text-muted-foreground mt-3 text-xs">
          SHA-256 <span className="font-mono break-all">{code.data.sha256_hex}</span>
        </p>
      </Section>
    )
  }

  const changed = (n: string) => !(n in baseline) || baseline[n] !== files[n]
  const removed = Object.keys(baseline).filter((n) => !(n in files))

  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          Code source
          {dirty && <span className="text-xs font-normal text-amber-700 dark:text-amber-400">Changes not deployed</span>}
        </span>
      }
      actions={
        <>
          {header}
          {dirty && (
            <Button variant="ghost" size="sm" onClick={() => setDiscarding(true)} disabled={deploying}>
              <Undo2 /> Discard
            </Button>
          )}
          <Button size="sm" onClick={deploy} disabled={!dirty || deploying} title="Deploy (Ctrl+S / Cmd+S)">
            {deploying ? <Loader2 className="animate-spin" /> : <Rocket />} Deploy
          </Button>
        </>
      }
      bodyClassName="flex flex-col gap-3"
    >
      {!handlerFound && (
        <div className="flex gap-2 rounded-md border border-amber-600/30 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-400/30 dark:bg-amber-500/10 dark:text-amber-300">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            The handler <span className="font-mono">{fn.handler}</span> expects{" "}
            {handlerCandidates.length > 1 ? "one of " : ""}
            <span className="font-mono">{handlerCandidates.slice(handlerCandidates.length > 1 ? 1 : 0).join(", ") || "file.function"}</span>, which is not
            in the package. Invocations will fail until you add it or change the handler in Configuration.
          </span>
        </div>
      )}

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <div role="tablist" aria-label="Files" className="flex min-w-0 flex-1 gap-1 overflow-x-auto border-b">
            {names.map((n) => (
              <button
                key={n}
                type="button"
                role="tab"
                aria-selected={n === current}
                onClick={() => setCurrent(n)}
                className={cn(
                  "-mb-px flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-1.5 font-mono text-[13px] whitespace-nowrap transition-colors",
                  n === current ? "border-primary text-foreground" : "text-muted-foreground hover:text-foreground border-transparent",
                )}
              >
                {n}
                {changed(n) && <span className="size-1.5 rounded-full bg-amber-500" aria-label="modified" />}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-1">
            <Button variant="ghost" size="sm" onClick={() => setNaming({ mode: "add" })} disabled={names.length >= 50}>
              <FilePlus2 /> New file
            </Button>
            <Button variant="ghost" size="sm" onClick={() => current && setNaming({ mode: "rename", from: current })} disabled={!current}>
              <Pencil /> Rename
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="text-destructive hover:text-destructive"
              onClick={() => current && setDeletingFile(current)}
              disabled={!current || names.length <= 1}
              title={names.length <= 1 ? "A function needs at least one file" : undefined}
            >
              <Trash2 /> Delete
            </Button>
          </div>
        </div>
        {current && current in files ? (
          <CodeEditor
            key={current}
            ariaLabel={`Contents of ${current}`}
            value={files[current]}
            onChange={(v) => setFiles((f) => ({ ...f, [current]: v }))}
          />
        ) : (
          <p className="text-muted-foreground rounded-md border p-8 text-center text-sm">The package has no files. Add one to get started.</p>
        )}
        <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
          <span>
            {pluralize(names.length, "file")}
            {dirty && (
              <>
                {" · "}
                {names.filter(changed).length} changed{removed.length ? `, ${removed.length} deleted` : ""}
              </>
            )}
          </span>
          <span>Tab indents, Shift+Tab outdents, Ctrl/Cmd+S deploys.</span>
        </div>
      </div>

      <FileNameDialog
        state={naming}
        existing={names}
        onClose={() => setNaming(null)}
        onSubmit={(name) => {
          if (naming?.mode === "rename" && naming.from) {
            const from = naming.from
            setFiles((f) => {
              const next: Files = {}
              for (const [k, v] of Object.entries(f)) next[k === from ? name : k] = v
              return next
            })
          } else {
            setFiles((f) => ({ ...f, [name]: "" }))
          }
          setCurrent(name)
          setNaming(null)
        }}
      />
      <ConfirmDialog
        open={!!deletingFile}
        onOpenChange={(o) => !o && setDeletingFile(null)}
        title={`Delete ${deletingFile ?? ""}?`}
        description="The file is removed from the editor. The function keeps its current code until you deploy."
        onConfirm={() => {
          if (!deletingFile) return
          const rest = names.filter((n) => n !== deletingFile)
          setFiles((f) => {
            const next = { ...f }
            delete next[deletingFile]
            return next
          })
          setCurrent(rest[0] ?? "")
        }}
      />
      <ConfirmDialog
        open={discarding}
        onOpenChange={setDiscarding}
        title="Discard changes?"
        description="Your edits are replaced with the deployed code."
        actionLabel="Discard changes"
        onConfirm={() => {
          setFiles(baseline)
          if (!(current in baseline)) setCurrent(sortNames(Object.keys(baseline))[0] ?? "")
        }}
      />
    </Section>
  )
}

function FileNameDialog({
  state,
  existing,
  onClose,
  onSubmit,
}: {
  state: { mode: "add" | "rename"; from?: string } | null
  existing: string[]
  onClose: () => void
  onSubmit: (name: string) => void
}) {
  const [name, setName] = useState("")
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (state) {
      setName(state.from ?? "")
      setTouched(false)
    }
  }, [state])
  const error = fileNameError(name, existing, state?.from)
  const rename = state?.mode === "rename"

  return (
    <Dialog open={!!state} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            setTouched(true)
            if (!error) onSubmit(name.trim())
          }}
          className="flex flex-col gap-4"
        >
          <DialogHeader>
            <DialogTitle>{rename ? `Rename ${state?.from}` : "New file"}</DialogTitle>
            <DialogDescription>Paths are relative to the package root (/var/task). Use / for folders, e.g. lib/helpers.py.</DialogDescription>
          </DialogHeader>
          <Field label="File name" htmlFor="file-name" error={touched || name ? error : undefined}>
            <Input
              id="file-name"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="utils.py"
            />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!!error || (rename && name.trim() === state?.from)}>
              {rename ? "Rename" : "Create file"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
