package workflows

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ---------------------------------------------------------------------------
// LoadGen
// ---------------------------------------------------------------------------

// LoadGenInput configures the load generator.
type LoadGenInput struct {
	// Mode is the branch backing type for all workload items.
	Mode BranchMode `json:"mode"`

	// TemplateID is the template to use for all exec calls.
	TemplateID string `json:"template_id"`

	// TotalItems is the total number of FsWorkloadItem workflows to spawn.
	TotalItems int `json:"total_items"`

	// ItemsPerSecond is the spawn rate. 0 means spawn all immediately.
	ItemsPerSecond float64 `json:"items_per_second"`
}

// LoadGenOutput is the result of the load generator.
type LoadGenOutput struct {
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Errors    []string `json:"errors,omitempty"`
}

// LoadGen spawns FsWorkloadItem child workflows at a configurable rate.
func LoadGen(ctx workflow.Context, input LoadGenInput) (LoadGenOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("LoadGen started",
		"mode", input.Mode,
		"total_items", input.TotalItems,
		"items_per_second", input.ItemsPerSecond,
	)

	if input.TotalItems <= 0 {
		return LoadGenOutput{}, fmt.Errorf("total_items must be > 0")
	}
	if input.TemplateID == "" {
		return LoadGenOutput{}, fmt.Errorf("template_id must not be empty")
	}

	childOpts := workflow.ChildWorkflowOptions{
		TaskQueue: "fs-workflow",
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	childCtx := workflow.WithChildOptions(ctx, childOpts)

	var futures []workflow.ChildWorkflowFuture
	for i := 0; i < input.TotalItems; i++ {
		if input.ItemsPerSecond > 0 && i > 0 {
			interval := time.Duration(float64(time.Second) / input.ItemsPerSecond)
			_ = workflow.Sleep(ctx, interval)
		}

		itemID := randomID(ctx)
		f := workflow.ExecuteChildWorkflow(childCtx, FsWorkloadItem, FsWorkloadItemInput{
			Mode:       input.Mode,
			TemplateID: input.TemplateID,
			ItemID:     itemID,
		})
		futures = append(futures, f)
	}

	var out LoadGenOutput
	for _, f := range futures {
		var itemOut FsWorkloadItemOutput
		if err := f.Get(ctx, &itemOut); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, err.Error())
		} else if itemOut.Error != "" {
			out.Failed++
			out.Errors = append(out.Errors, itemOut.Error)
		} else {
			out.Succeeded++
		}
	}

	logger.Info("LoadGen completed", "succeeded", out.Succeeded, "failed", out.Failed)
	return out, nil
}

// ---------------------------------------------------------------------------
// FsWorkloadItem
// ---------------------------------------------------------------------------

// FsWorkloadItemInput is the input for a single workload item.
type FsWorkloadItemInput struct {
	Mode       BranchMode `json:"mode"`
	TemplateID string     `json:"template_id"`
	ItemID     string     `json:"item_id"`
}

// FsWorkloadItemOutput is the result of a single workload item.
type FsWorkloadItemOutput struct {
	Error string `json:"error,omitempty"`
}

const (
	loadgenTotalLines = 1000
	loadgenReadLines  = 100
	loadgenFilePath   = "/data/testfile.txt"
)

// FsWorkloadItem creates a BranchSession, writes a 1MB file with
// deterministic content, reads a random 100-line range, and verifies the
// read content matches expected values.
func FsWorkloadItem(ctx workflow.Context, input FsWorkloadItemInput) (FsWorkloadItemOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("FsWorkloadItem started", "item_id", input.ItemID, "mode", input.Mode)

	session := NewBranchSession(ctx, input.Mode)

	// -----------------------------------------------------------------
	// Step 1: Write a file with 1000 lines of deterministic content.
	// -----------------------------------------------------------------
	writeCmd := buildWriteScript(input.ItemID)

	writeResp, err := session.Exec(ctx, input.TemplateID, writeCmd)
	if err != nil {
		return FsWorkloadItemOutput{Error: fmt.Sprintf("write exec: %v", err)}, nil
	}
	if writeResp.ExitCode != 0 {
		return FsWorkloadItemOutput{
			Error: fmt.Sprintf("write failed (exit %d): stdout=%s stderr=%s",
				writeResp.ExitCode, writeResp.Stdout, writeResp.Stderr),
		}, nil
	}

	logger.Info("FsWorkloadItem: write completed", "item_id", input.ItemID)

	// -----------------------------------------------------------------
	// Step 2: Read a random 100-line range from the file.
	// -----------------------------------------------------------------
	var startLine int
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return rand.Intn(loadgenTotalLines - loadgenReadLines)
	}).Get(&startLine)

	readCmd := buildReadScript(startLine)

	readResp, err := session.Exec(ctx, input.TemplateID, readCmd)
	if err != nil {
		return FsWorkloadItemOutput{Error: fmt.Sprintf("read exec: %v", err)}, nil
	}
	if readResp.ExitCode != 0 {
		return FsWorkloadItemOutput{
			Error: fmt.Sprintf("read failed (exit %d): stdout=%s stderr=%s",
				readResp.ExitCode, readResp.Stdout, readResp.Stderr),
		}, nil
	}

	logger.Info("FsWorkloadItem: read completed", "item_id", input.ItemID, "start_line", startLine)

	// -----------------------------------------------------------------
	// Step 3: Verify the content matches expected values.
	// -----------------------------------------------------------------
	var expected string
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return buildExpectedOutput(input.ItemID, startLine, loadgenReadLines)
	}).Get(&expected)

	actual := strings.TrimRight(readResp.Stdout, "\n")
	if actual != expected {
		actualLines := strings.Split(actual, "\n")
		expectedLines := strings.Split(expected, "\n")
		mismatch := fmt.Sprintf("line count: expected=%d actual=%d", len(expectedLines), len(actualLines))
		for i := 0; i < len(expectedLines) && i < len(actualLines); i++ {
			if actualLines[i] != expectedLines[i] {
				mismatch = fmt.Sprintf("first mismatch at line %d: expected=%q actual=%q",
					startLine+i, expectedLines[i], actualLines[i])
				break
			}
		}
		return FsWorkloadItemOutput{
			Error: fmt.Sprintf("verification failed: %s", mismatch),
		}, nil
	}

	logger.Info("FsWorkloadItem: verification passed", "item_id", input.ItemID)
	return FsWorkloadItemOutput{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildWriteScript returns a Python command that writes loadgenTotalLines
// lines of deterministic content to loadgenFilePath. Each line is
// "<line_number>:<sha256_hex[:64]>" where the hash input is "<itemID>:<i>".
func buildWriteScript(itemID string) string {
	return fmt.Sprintf(`python3 -c "
import hashlib
with open('%s', 'w') as f:
    for i in range(%d):
        h = hashlib.sha256(('%s:' + str(i)).encode()).hexdigest()[:64]
        f.write(str(i) + ':' + h + '\n')
print('wrote %d lines')
"`, loadgenFilePath, loadgenTotalLines, itemID, loadgenTotalLines)
}

// buildReadScript returns a sed command that reads loadgenReadLines lines
// starting at startLine (0-indexed) from loadgenFilePath.
func buildReadScript(startLine int) string {
	from := startLine + 1 // sed is 1-indexed
	to := startLine + loadgenReadLines
	return fmt.Sprintf(`sed -n '%d,%dp' %s`, from, to, loadgenFilePath)
}

// buildExpectedOutput computes the expected file content for a line range,
// matching the Python hashlib.sha256 output used in buildWriteScript.
func buildExpectedOutput(itemID string, startLine, count int) string {
	var b strings.Builder
	for i := startLine; i < startLine+count; i++ {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", itemID, i))))
		if i > startLine {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d:%s", i, h[:64])
	}
	return b.String()
}
