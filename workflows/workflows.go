package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "fs-worker"

// BranchMode determines whether a branch is backed by a ZFS zvol (block
// device) or a ZFS dataset (filesystem).
type BranchMode string

const (
	BranchModeZvol BranchMode = "zvol"
	BranchModeZDS  BranchMode = "zds"
)

// ---------------------------------------------------------------------------
// InitBranch
// ---------------------------------------------------------------------------

type InitBranchInput struct {
	ID   string     `json:"id"`
	Mode BranchMode `json:"mode"`
}

// ---------------------------------------------------------------------------
// Exec
// ---------------------------------------------------------------------------

type ExecInput struct {
	ID             string     `json:"id"`
	Mode           BranchMode `json:"mode"`
	TemplateID     string     `json:"template_id"`
	Cmd            string     `json:"cmd"`
	TargetSnapshot string     `json:"target_snapshot"`
	BaseSnapshot   string     `json:"base_snapshot"`
	UseSnapshot    bool       `json:"use_snapshot"`
}

type ExecOutput struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// ---------------------------------------------------------------------------
// CreateTemplate
// ---------------------------------------------------------------------------

type CreateTemplateInput struct {
	ID  string `json:"id"`
	Cmd string `json:"cmd"`
}

type CreateTemplateOutput struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// ---------------------------------------------------------------------------
// Activity options
// ---------------------------------------------------------------------------

func defaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		TaskQueue:           TaskQueue,
		StartToCloseTimeout: 20 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    1,
		},
	}
}

func longActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		TaskQueue:           TaskQueue,
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    1,
		},
	}
}

// ---------------------------------------------------------------------------
// Workflows
// ---------------------------------------------------------------------------

func InitBranch(ctx workflow.Context, input InitBranchInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("InitBranch workflow started", "id", input.ID, "mode", input.Mode)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	err := workflow.ExecuteActivity(actCtx, "InitBranch", InitBranchInput{
		ID:   input.ID,
		Mode: input.Mode,
	}).Get(ctx, nil)
	if err != nil {
		logger.Error("InitBranch activity failed", "id", input.ID, "error", err)
		return err
	}

	logger.Info("InitBranch workflow completed", "id", input.ID)
	return nil
}

func Exec(ctx workflow.Context, input ExecInput) (ExecOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Exec workflow started", "id", input.ID, "mode", input.Mode, "cmd", input.Cmd)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	var result ExecOutput
	err := workflow.ExecuteActivity(actCtx, "Exec", ExecInput{
		ID:             input.ID,
		Mode:           input.Mode,
		TemplateID:     input.TemplateID,
		Cmd:            input.Cmd,
		TargetSnapshot: input.TargetSnapshot,
		BaseSnapshot:   input.BaseSnapshot,
		UseSnapshot:    input.UseSnapshot,
	}).Get(ctx, &result)
	if err != nil {
		logger.Error("Exec activity failed", "id", input.ID, "error", err)
		return ExecOutput{}, err
	}

	logger.Info("Exec workflow completed", "id", input.ID, "exit_code", result.ExitCode)
	return result, nil
}

func CreateTemplate(ctx workflow.Context, input CreateTemplateInput) (CreateTemplateOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("CreateTemplate workflow started", "id", input.ID, "cmd", input.Cmd)

	actCtx := workflow.WithActivityOptions(ctx, longActivityOptions())

	var result CreateTemplateOutput
	err := workflow.ExecuteActivity(actCtx, "CreateTemplate", CreateTemplateInput{
		ID:  input.ID,
		Cmd: input.Cmd,
	}).Get(ctx, &result)
	if err != nil {
		logger.Error("CreateTemplate activity failed", "id", input.ID, "error", err)
		return CreateTemplateOutput{}, err
	}

	logger.Info("CreateTemplate workflow completed", "id", input.ID, "exit_code", result.ExitCode)
	return result, nil
}
