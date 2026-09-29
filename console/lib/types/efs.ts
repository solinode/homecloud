import type { Tags } from "./common"

export interface FileSystem {
  id: string
  arn: string
  name: string
  state: string
  /** Instances can only mount a read-only file system read-only. */
  read_only: boolean
  created_at: string
  size_bytes: number
  /** When size_bytes was measured (zero time if never); GET measures at most once a minute. */
  size_updated?: string
  tags?: Tags | null
  /** IDs of non-terminated instances that mount the file system. */
  mounted_by: string[]
}

export interface CreateFileSystemInput {
  name?: string
  read_only?: boolean
  tags?: Tags
}

/** FileSystemMount attaches a file system to an instance at launch (RunInstances file_systems). */
export interface FileSystemMount {
  file_system_id: string
  mount_path: string
  read_only: boolean
}
