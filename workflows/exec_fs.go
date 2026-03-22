package workflows

import (
	"fmt"
	"math/rand"

	"go.temporal.io/sdk/workflow"
)

const initSnapshot = "__init"

const snapIDLen = 6

var snapIDChars = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func randomID(ctx workflow.Context) string {
	var id string
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		b := make([]byte, snapIDLen)
		for i := range b {
			b[i] = snapIDChars[rand.Intn(len(snapIDChars))]
		}
		return string(b)
	}).Get(&id)
	return id
}

// ---------------------------------------------------------------------------
// BranchSession — reusable branch + snapshot state
// ---------------------------------------------------------------------------

// BranchSession manages a ZFS branch and its snapshot chain. It is used by
// both the ExecFs workflow (via update handler) and FsWorkloadItem (directly).
type BranchSession struct {
	BranchID        string
	Mode            BranchMode
	CurrentSnapshot string
	SnapshotHistory []SnapshotRecord
}

// NewBranchSession creates a new session with a randomly generated branch ID.
func NewBranchSession(ctx workflow.Context, mode BranchMode) *BranchSession {
	return &BranchSession{
		BranchID: randomID(ctx),
		Mode:     mode,
	}
}

// Exec runs a command via the Exec activity, managing base/target snapshots
// automatically. On success the snapshot advances; on failure the activity
// rolls back and the session snapshot stays unchanged.
func (s *BranchSession) Exec(ctx workflow.Context, templateID, cmd string) (ExecFsExecResponse, error) {
	baseSnapshot := s.CurrentSnapshot
	if baseSnapshot == "" {
		baseSnapshot = initSnapshot
	}

	targetSnapshot := randomID(ctx)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	var result ExecOutput
	err := workflow.ExecuteActivity(actCtx, "Exec", ExecInput{
		ID:             s.BranchID,
		Mode:           s.Mode,
		TemplateID:     templateID,
		Cmd:            cmd,
		TargetSnapshot: targetSnapshot,
		BaseSnapshot:   baseSnapshot,
	}).Get(ctx, &result)
	if err != nil {
		return ExecFsExecResponse{}, fmt.Errorf("exec activity failed: %w", err)
	}

	if result.ExitCode == 0 {
		s.CurrentSnapshot = targetSnapshot
		s.SnapshotHistory = append(s.SnapshotHistory, SnapshotRecord{
			Snapshot: targetSnapshot,
			Cmd:      cmd,
			ExitCode: result.ExitCode,
		})
	}

	return ExecFsExecResponse{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
	}, nil
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ExecFsInput is the input payload for the ExecFs workflow.
type ExecFsInput struct {
	Mode BranchMode `json:"mode"`
}

// ExecFsExecRequest is the payload sent via the "Exec" update handler.
type ExecFsExecRequest struct {
	TemplateID string `json:"template_id"`
	Cmd        string `json:"cmd"`
}

// ExecFsExecResponse is returned by the "Exec" update handler.
type ExecFsExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// SnapshotRecord is a single entry in the snapshot history.
type SnapshotRecord struct {
	Snapshot string `json:"snapshot"`
	Cmd      string `json:"cmd"`
	ExitCode int    `json:"exit_code"`
}

// ---------------------------------------------------------------------------
// ExecFs workflow
// ---------------------------------------------------------------------------

// ExecFs is a long-running workflow that manages a filesystem branch and
// exposes an "Exec" update handler. The branch is created lazily on the first
// Exec call. The workflow tracks the current snapshot and generates random
// target snapshot IDs for each execution.
func ExecFs(ctx workflow.Context, input ExecFsInput) error {
	logger := workflow.GetLogger(ctx)

	session := NewBranchSession(ctx, input.Mode)
	logger.Info("ExecFs workflow started", "branch_id", session.BranchID, "mode", input.Mode)

	err := workflow.SetQueryHandler(ctx, "Snapshots", func() ([]SnapshotRecord, error) {
		return session.SnapshotHistory, nil
	})
	if err != nil {
		return fmt.Errorf("register Snapshots query handler: %w", err)
	}

	err = workflow.SetUpdateHandlerWithOptions(ctx, "Exec",
		func(ctx workflow.Context, req ExecFsExecRequest) (ExecFsExecResponse, error) {
			return session.Exec(ctx, req.TemplateID, req.Cmd)
		},
		workflow.UpdateHandlerOptions{
			Validator: func(ctx workflow.Context, req ExecFsExecRequest) error {
				if req.TemplateID == "" {
					return fmt.Errorf("template_id must not be empty")
				}
				if req.Cmd == "" {
					return fmt.Errorf("cmd must not be empty")
				}
				return nil
			},
		},
	)
	if err != nil {
		return fmt.Errorf("register Exec update handler: %w", err)
	}

	_ = workflow.GetSignalChannel(ctx, "shutdown").Receive(ctx, nil)

	logger.Info("ExecFs workflow shutting down", "branch_id", session.BranchID)
	return nil
}
