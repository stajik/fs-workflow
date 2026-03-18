package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/filesystem/fs-worker/activities"
)

const TaskQueue = "fs-worker"

type InitBranchInput struct {
	ID   string                `json:"id"`
	Mode activities.BranchMode `json:"mode"`
}

func defaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		TaskQueue:           TaskQueue,
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
		},
	}
}

func InitBranch(ctx workflow.Context, input InitBranchInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("InitBranch workflow started", "id", input.ID, "mode", input.Mode)

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	err := workflow.ExecuteActivity(actCtx, "InitBranch", activities.InitBranchInput{
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
