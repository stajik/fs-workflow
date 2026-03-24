# fs-workflow

A Temporal **workflow** worker that orchestrates branch lifecycle operations
by calling the activities defined in [fs-worker](../fs-worker).

The workflow worker runs on its own task queue (`fs-workflow`) and dispatches
activity tasks to the `fs-worker` task queue, where the
[fs-worker](../fs-worker) activity worker picks them up and executes the
actual ZFS operations.

## Architecture

```
┌──────────────┐         ┌─────────────────┐         ┌──────────────┐
│   Temporal   │◄───────►│  fs-workflow     │────────►│  fs-worker   │
│   Server     │         │  (workflow       │ activity│  (activity   │
│              │         │   worker)        │  tasks  │   worker)    │
└──────────────┘         └─────────────────┘         └──────────────┘
     task queue:              fs-workflow                 fs-worker
```

## Workflows

### `InitBranch`

Creates a ZFS branch by calling the `InitBranch` activity on the fs-worker.
The branch can be backed by either a zvol (block device) or a ZFS dataset
(filesystem), depending on the `mode` parameter.

**Input**

```json
{
  "id": "my-branch",
  "mode": "zvol"
}
```

| Field  | Type   | Description                                                        |
|--------|--------|--------------------------------------------------------------------|
| `id`   | string | Unique identifier for the branch.                                  |
| `mode` | string | Branch backing type: `"zvol"` (block device) or `"zds"` (dataset). |

**Output (zvol mode)**

```json
{
  "dataset": "testpool/my-branch",
  "device": "/dev/zvol/testpool/my-branch",
  "fs_type": "ext4",
  "mode": "zvol"
}
```

**Output (zds mode)**

```json
{
  "dataset": "testpool/my-branch",
  "mount_point": "/testpool/my-branch",
  "fs_type": "zfs",
  "mode": "zds"
}
```

---

### `Exec`

Executes a single command inside a sandboxed filesystem branch. Manages
snapshot creation so that the filesystem state is captured before and after
execution. This is the low-level, one-shot execution workflow.

**Input**

```json
{
  "id": "my-branch",
  "mode": "zvol",
  "template_id": "ubuntu-base",
  "cmd": "echo hello",
  "target_snapshot": "snap-abc123",
  "base_snapshot": "__init",
  "use_snapshot": true
}
```

| Field             | Type   | Description                                                           |
|-------------------|--------|-----------------------------------------------------------------------|
| `id`              | string | Branch identifier.                                                    |
| `mode`            | string | Branch backing type: `"zvol"` or `"zds"`.                             |
| `template_id`     | string | Template to use for execution environment.                            |
| `cmd`             | string | The command to execute inside the sandbox.                            |
| `target_snapshot` | string | Name for the snapshot taken after execution.                          |
| `base_snapshot`   | string | Snapshot to restore from before execution.                            |
| `use_snapshot`    | bool   | Whether to use snapshot-based restore.                                |

**Output**

```json
{
  "exit_code": 0,
  "stdout": "hello\n",
  "stderr": ""
}
```

| Field       | Type   | Description                        |
|-------------|--------|------------------------------------|
| `exit_code` | int    | Process exit code.                 |
| `stdout`    | string | Standard output of the command.    |
| `stderr`    | string | Standard error of the command.     |

---

### `CreateTemplate`

Creates a reusable filesystem template by executing a setup command (e.g.
installing packages, configuring the environment). The resulting template can
be referenced by `template_id` in subsequent `Exec` or `ExecFs` calls.

**Input**

```json
{
  "id": "ubuntu-base",
  "cmd": "apt-get update && apt-get install -y python3"
}
```

| Field | Type   | Description                                      |
|-------|--------|--------------------------------------------------|
| `id`  | string | Unique identifier for the template.              |
| `cmd` | string | Setup command to run when creating the template.  |

**Output**

```json
{
  "exit_code": 0,
  "stdout": "...",
  "stderr": ""
}
```

| Field       | Type   | Description                        |
|-------------|--------|------------------------------------|
| `exit_code` | int    | Process exit code.                 |
| `stdout`    | string | Standard output of the command.    |
| `stderr`    | string | Standard error of the command.     |

---

### `ExecFs`

A **long-running** workflow that manages a filesystem branch over its entire
lifetime. Rather than executing a single command and completing, `ExecFs`
keeps running and exposes an `Exec` **update handler** that callers can
invoke repeatedly to run commands against the same branch.

Each successful execution advances the snapshot, building up a chain of
filesystem states. The workflow also exposes a `Snapshots` **query handler**
to inspect the snapshot history, and shuts down gracefully when it receives a
`shutdown` signal.

**Workflow Input**

```json
{
  "mode": "zvol"
}
```

| Field  | Type   | Description                                                        |
|--------|--------|--------------------------------------------------------------------|
| `mode` | string | Branch backing type: `"zvol"` (block device) or `"zds"` (dataset). |

**Update Handler — `Exec`**

Send commands to the running workflow via the `Exec` update:

```json
{
  "template_id": "ubuntu-base",
  "cmd": "echo hello"
}
```

| Field         | Type   | Description                                     |
|---------------|--------|-------------------------------------------------|
| `template_id` | string | Template to use for execution (required).       |
| `cmd`         | string | The command to execute (required).               |

The update returns:

```json
{
  "exit_code": 0,
  "stdout": "hello\n",
  "stderr": ""
}
```

**Query Handler — `Snapshots`**

Returns the full snapshot history as an array of records:

```json
[
  {
    "snapshot": "aB3xZq",
    "cmd": "echo hello",
    "exit_code": 0
  }
]
```

**Signal — `shutdown`**

Send a `shutdown` signal to gracefully terminate the workflow.

---

### `LoadGen`

A load-generation workflow that spawns multiple `FsWorkloadItem` child
workflows at a configurable rate. Useful for stress-testing and benchmarking
the filesystem infrastructure.

**Input**

```json
{
  "mode": "zvol",
  "template_id": "ubuntu-base",
  "total_items": 100,
  "items_per_second": 10.0
}
```

| Field              | Type   | Description                                                      |
|--------------------|--------|------------------------------------------------------------------|
| `mode`             | string | Branch backing type for all workload items.                      |
| `template_id`      | string | Template to use for all exec calls.                              |
| `total_items`      | int    | Total number of `FsWorkloadItem` workflows to spawn.             |
| `items_per_second`  | float  | Spawn rate. `0` means spawn all immediately.                     |

**Output**

```json
{
  "succeeded": 95,
  "failed": 5,
  "errors": ["write exec: activity timeout", "..."]
}
```

| Field       | Type     | Description                                    |
|-------------|----------|------------------------------------------------|
| `succeeded` | int      | Number of workload items that passed.          |
| `failed`    | int      | Number of workload items that failed.          |
| `errors`    | []string | Error messages from failed items (if any).     |

---

### `FsWorkloadItem`

A single workload item spawned by `LoadGen`. Each item creates its own
`BranchSession`, writes a deterministic 1000-line file using Python's
`hashlib.sha256`, reads back a random 100-line range via `sed`, and verifies
the content matches the expected hash values.

**Input**

```json
{
  "mode": "zvol",
  "template_id": "ubuntu-base",
  "item_id": "aB3xZq"
}
```

| Field         | Type   | Description                                     |
|---------------|--------|-------------------------------------------------|
| `mode`        | string | Branch backing type.                            |
| `template_id` | string | Template to use for exec calls.                 |
| `item_id`     | string | Unique identifier for this workload item.       |

**Output**

```json
{
  "error": ""
}
```

| Field   | Type   | Description                                              |
|---------|--------|----------------------------------------------------------|
| `error` | string | Empty on success; contains error message on failure.     |

## Requirements

- Go 1.23+
- A running [Temporal](https://temporal.io/) server
- The [fs-worker](../fs-worker) activity worker running on the `fs-worker` task queue

## Configuration

All configuration is via environment variables:

| Variable              | Default            | Description                        |
|-----------------------|--------------------|------------------------------------|
| `TEMPORAL_HOST`       | `localhost:7233`   | Temporal server gRPC endpoint.     |
| `TEMPORAL_NAMESPACE`  | `default`          | Temporal namespace.                |
| `TEMPORAL_TASK_QUEUE` | `fs-workflow`      | Task queue this worker listens on. |

## Building

```bash
make build
```

## Running

```bash
# Start the workflow worker (ensure Temporal and fs-worker are already running)
./fs-workflow
```

Or with custom configuration:

```bash
TEMPORAL_HOST=temporal.example.com:7233 ./fs-workflow
```

## Project structure

```
fs-workflow/
├── main.go              # Workflow worker entry point
├── workflows/
│   ├── workflows.go     # Shared types, InitBranch, Exec, CreateTemplate
│   ├── exec_fs.go       # ExecFs workflow, BranchSession, update/query handlers
│   └── loadgen.go       # LoadGen and FsWorkloadItem workflows
├── go.mod
├── go.sum
├── Makefile
└── README.md
```
