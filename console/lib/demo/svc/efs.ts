import { badRequest, err, getState, notFound, type DemoService, type Router } from "../engine"
import { DAY, MIN, ago, arn, uid } from "../util"
import { FS } from "../ids"
import type { FileSystem, Instance, Tags } from "@/lib/types"

// Amazon EFS file systems (/api/v1/efs/file-systems).

type FsRec = Omit<FileSystem, "mounted_by">
interface EfsState {
  fileSystems: FsRec[]
}
const efsState = () => getState().efs as EfsState

const GB = 1024 ** 3

function seed(): EfsState {
  const fs = (id: string, name: string, age: number, sizeBytes: number, readOnly: boolean, tags: Tags): FsRec => ({
    id, arn: arn("elasticfilesystem", `file-system/${id}`), name, state: "available", read_only: readOnly, created_at: ago(age), size_bytes: sizeBytes, size_updated: ago(4 * MIN), tags,
  })
  return {
    fileSystems: [
      fs(FS.media, "shop-media", 90 * DAY, Math.round(41.7 * GB), false, { Name: "shop-media", Environment: "production", Project: "shop" }),
      fs(FS.backups, "shop-backups", 62 * DAY, Math.round(212.4 * GB), true, { Name: "shop-backups", Environment: "production", Project: "shop", Retention: "90d" }),
      fs(FS.scratch, "dev-scratch", 14 * DAY, Math.round(1.3 * GB), false, { Environment: "dev", Owner: "alice" }),
    ],
  }
}

const mountedBy = (id: string): string[] =>
  ((getState().ec2?.instances ?? []) as Instance[]).filter((i) => i.state !== "terminated" && (i.file_systems ?? []).some((m) => m.file_system_id === id)).map((i) => i.id)

const view = (f: FsRec): FileSystem => ({ ...f, tags: f.tags ?? null, mounted_by: mountedBy(f.id) })

function routes(r: Router) {
  const P = "/api/v1/efs/file-systems"
  r.get(P, () => efsState().fileSystems.map(view))
  r.get(`${P}/:id`, ({ params }) => {
    const f = efsState().fileSystems.find((x) => x.id === params.id)
    if (!f) throw notFound("file system", params.id)
    // the real API re-measures at most once a minute
    if (Date.now() - new Date(f.size_updated ?? 0).getTime() > MIN) f.size_updated = new Date().toISOString()
    return view(f)
  })
  r.post(P, ({ body }) => {
    const name = String(body?.name ?? "")
    if (!/^[\w .+=@/-]{0,128}$/.test(name)) throw badRequest("invalid file system name")
    const id = uid("fs-")
    const f: FsRec = {
      id, arn: arn("elasticfilesystem", `file-system/${id}`), name, state: "available", read_only: !!body?.read_only, created_at: new Date().toISOString(), size_bytes: 0,
      size_updated: new Date().toISOString(), tags: body?.tags ?? null,
    }
    efsState().fileSystems.push(f)
    return view(f)
  })
  r.del(`${P}/:id`, ({ params }) => {
    const s = efsState()
    const f = s.fileSystems.find((x) => x.id === params.id)
    if (!f) throw notFound("file system", params.id)
    const m = mountedBy(f.id)
    if (m.length) throw err(409, "FileSystemInUse", `file system ${f.id} is mounted by ${m.join(", ")}`)
    s.fileSystems = s.fileSystems.filter((x) => x.id !== f.id)
    return null
  })
}

const service: DemoService = { name: "efs", seed, routes }
export default service

