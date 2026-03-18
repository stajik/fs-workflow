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
│   └── workflows.go     # Workflow definition and types
├── go.mod
├── go.sum
├── Makefile
└── README.md
```
