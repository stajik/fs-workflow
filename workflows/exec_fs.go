package workflows

import (
	"fmt"
	"math/rand"

	"go.temporal.io/sdk/workflow"
)

const initSnapshot = "__init"

const snapIDLen = 6

var snapIDChars = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

// ExecFsInput is the input payload for the ExecFs workflow.
type ExecFsInput struct {
	// Mode is the branch backing type: "zvol" or "zds".
	Mode BranchMode `json:"mode"`
}

// ExecFsExecRequest is the payload sent via the "Exec" update handler.
type ExecFsExecRequest struct {
	// TemplateID is the template to boot from.
	TemplateID string `json:"template_id"`

	// Cmd is the command to execute inside the VM.
	Cmd string `json:"cmd"`
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

func randomID(ctx workflow.Context) string {
	// SideEffect ensures the random value is recorded in history and replayed
	// deterministically.
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

// ExecFs is a long-running workflow that manages a filesystem branch and
// exposes an "Exec" update handler. The branch is created lazily on the first
// Exec call. The workflow tracks the current snapshot and generates random
// target snapshot IDs for each execution.
func ExecFs(ctx workflow.Context, input ExecFsInput) error {
	logger := workflow.GetLogger(ctx)

	branchID := randomID(ctx)
	logger.Info("ExecFs workflow started", "branch_id", branchID, "mode", input.Mode)

	currentSnapshot := "" // empty means no branch yet
	var snapshotHistory []SnapshotRecord

	err := workflow.SetQueryHandler(ctx, "Snapshots", func() ([]SnapshotRecord, error) {
		return snapshotHistory, nil
	})
	if err != nil {
		return fmt.Errorf("register Snapshots query handler: %w", err)
	}

	err = workflow.SetUpdateHandlerWithOptions(ctx, "Exec",
		func(ctx workflow.Context, req ExecFsExecRequest) (ExecFsExecResponse, error) {
			baseSnapshot := currentSnapshot
			if baseSnapshot == "" {
				baseSnapshot = initSnapshot
			}

			targetSnapshot := randomID(ctx)

			actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

			var result ExecOutput
			err := workflow.ExecuteActivity(actCtx, "Exec", ExecInput{
				ID:             branchID,
				Mode:           input.Mode,
				TemplateID:     req.TemplateID,
				Cmd:            req.Cmd,
				TargetSnapshot: targetSnapshot,
				BaseSnapshot:   baseSnapshot,
			}).Get(ctx, &result)
			if err != nil {
				return ExecFsExecResponse{}, fmt.Errorf("exec activity failed: %w", err)
			}

			if result.ExitCode == 0 {
				currentSnapshot = targetSnapshot
				snapshotHistory = append(snapshotHistory, SnapshotRecord{
					Snapshot: targetSnapshot,
					Cmd:      req.Cmd,
					ExitCode: result.ExitCode,
				})
			}
			// On failure the activity already rolled back to base; snapshot unchanged.

			return ExecFsExecResponse{
				ExitCode: result.ExitCode,
				Stdout:   result.Stdout,
				Stderr:   result.Stderr,
			}, nil
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

	// Keep the workflow alive until cancelled.
	_ = workflow.GetSignalChannel(ctx, "shutdown").Receive(ctx, nil)

	logger.Info("ExecFs workflow shutting down", "branch_id", branchID)
	return nil
}
