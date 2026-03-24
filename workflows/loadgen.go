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

	// Pattern is a string of r/w/a characters defining the operation sequence
	// for each workload item. 'w' = write (overwrite), 'r' = read + verify,
	// 'a' = append. Example: "wraw" means write, read, append, write.
	// Defaults to "wr" if empty.
	Pattern string `json:"pattern,omitempty"`
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

	pattern := input.Pattern
	if pattern == "" {
		pattern = "wr"
	}
	for _, c := range pattern {
		if c != 'r' && c != 'w' && c != 'a' {
			return LoadGenOutput{}, fmt.Errorf("invalid pattern character %q: must be r, w, or a", c)
		}
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
			Pattern:    pattern,
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
	Pattern    string     `json:"pattern"`
}

// FsWorkloadItemOutput is the result of a single workload item.
type FsWorkloadItemOutput struct {
	Error string `json:"error,omitempty"`
}

const (
	loadgenTotalLines  = 1000
	loadgenAppendLines = 200
	loadgenReadLines   = 100
	loadgenFilePath    = "/data/testfile.txt"
)

// FsWorkloadItem creates a BranchSession and executes the operation pattern.
// Each character in the pattern triggers: 'w' = overwrite the file,
// 'r' = read a random range and verify, 'a' = append to the file.
func FsWorkloadItem(ctx workflow.Context, input FsWorkloadItemInput) (FsWorkloadItemOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("FsWorkloadItem started", "item_id", input.ItemID, "mode", input.Mode, "pattern", input.Pattern)

	session := NewBranchSession(ctx, input.Mode)

	// writeGen tracks how many writes/appends have occurred so we can
	// compute the expected file content for reads.
	writeGen := 0
	totalLines := 0

	for step, op := range input.Pattern {
		switch op {
		case 'w':
			writeCmd := buildWriteScript(input.ItemID, writeGen)
			resp, err := session.Exec(ctx, input.TemplateID, writeCmd)
			if err != nil {
				return FsWorkloadItemOutput{Error: fmt.Sprintf("step %d write exec: %v", step, err)}, nil
			}
			if resp.ExitCode != 0 {
				return FsWorkloadItemOutput{
					Error: fmt.Sprintf("step %d write failed (exit %d): stdout=%s stderr=%s",
						step, resp.ExitCode, resp.Stdout, resp.Stderr),
				}, nil
			}
			writeGen++
			totalLines = loadgenTotalLines
			logger.Info("FsWorkloadItem: write completed", "item_id", input.ItemID, "step", step, "write_gen", writeGen)

		case 'a':
			appendCmd := buildAppendScript(input.ItemID, writeGen, totalLines)
			resp, err := session.Exec(ctx, input.TemplateID, appendCmd)
			if err != nil {
				return FsWorkloadItemOutput{Error: fmt.Sprintf("step %d append exec: %v", step, err)}, nil
			}
			if resp.ExitCode != 0 {
				return FsWorkloadItemOutput{
					Error: fmt.Sprintf("step %d append failed (exit %d): stdout=%s stderr=%s",
						step, resp.ExitCode, resp.Stdout, resp.Stderr),
				}, nil
			}
			writeGen++
			totalLines += loadgenAppendLines
			logger.Info("FsWorkloadItem: append completed", "item_id", input.ItemID, "step", step, "write_gen", writeGen, "total_lines", totalLines)

		case 'r':
			if totalLines == 0 {
				return FsWorkloadItemOutput{Error: fmt.Sprintf("step %d read: no data written yet", step)}, nil
			}

			readLines := loadgenReadLines
			if readLines > totalLines {
				readLines = totalLines
			}

			var startLine int
			_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
				return rand.Intn(totalLines - readLines + 1)
			}).Get(&startLine)

			readCmd := buildReadScript(startLine, readLines)
			resp, err := session.Exec(ctx, input.TemplateID, readCmd)
			if err != nil {
				return FsWorkloadItemOutput{Error: fmt.Sprintf("step %d read exec: %v", step, err)}, nil
			}
			if resp.ExitCode != 0 {
				return FsWorkloadItemOutput{
					Error: fmt.Sprintf("step %d read failed (exit %d): stdout=%s stderr=%s",
						step, resp.ExitCode, resp.Stdout, resp.Stderr),
				}, nil
			}

			var expected string
			_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
				return buildExpectedOutput(input.ItemID, writeGen, startLine, readLines, totalLines)
			}).Get(&expected)

			actual := strings.TrimRight(resp.Stdout, "\n")
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
					Error: fmt.Sprintf("step %d verification failed: %s", step, mismatch),
				}, nil
			}

			logger.Info("FsWorkloadItem: read verified", "item_id", input.ItemID, "step", step, "start_line", startLine)
		}
	}

	logger.Info("FsWorkloadItem: all steps completed", "item_id", input.ItemID)
	return FsWorkloadItemOutput{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildWriteScript returns a Python command that writes loadgenTotalLines
// lines of deterministic content to loadgenFilePath. Each line is
// "<line_number>:<gen>:<sha256_hex[:64]>" where the hash input is
// "<itemID>:<gen>:<i>". gen distinguishes successive writes/appends.
func buildWriteScript(itemID string, gen int) string {
	return fmt.Sprintf(`python3 -c "
import hashlib
with open('%s', 'w') as f:
    for i in range(%d):
        h = hashlib.sha256(('%s:%d:' + str(i)).encode()).hexdigest()[:64]
        f.write(str(i) + ':%d:' + h + '\n')
print('wrote %d lines')
"`, loadgenFilePath, loadgenTotalLines, itemID, gen, gen, loadgenTotalLines)
}

// buildAppendScript returns a Python command that appends loadgenAppendLines
// lines to loadgenFilePath. Line numbering continues from startLineNum.
func buildAppendScript(itemID string, gen, startLineNum int) string {
	return fmt.Sprintf(`python3 -c "
import hashlib
with open('%s', 'a') as f:
    for i in range(%d, %d):
        h = hashlib.sha256(('%s:%d:' + str(i)).encode()).hexdigest()[:64]
        f.write(str(i) + ':%d:' + h + '\n')
print('appended %d lines')
"`, loadgenFilePath, startLineNum, startLineNum+loadgenAppendLines, itemID, gen, gen, loadgenAppendLines)
}

// buildReadScript returns a sed command that reads count lines
// starting at startLine (0-indexed) from loadgenFilePath.
func buildReadScript(startLine, count int) string {
	from := startLine + 1 // sed is 1-indexed
	to := startLine + count
	return fmt.Sprintf(`sed -n '%d,%dp' %s`, from, to, loadgenFilePath)
}

// buildExpectedOutput computes the expected file content for a line range.
// It reconstructs what the file looks like after the full write/append
// sequence up to the given writeGen. The file has totalLines lines total.
// The last write (w) overwrites with loadgenTotalLines lines at gen G,
// and each subsequent append (a) adds loadgenAppendLines lines at gen G+1, etc.
//
// We work backwards from writeGen to find which gen produced each line.
func buildExpectedOutput(itemID string, writeGen, startLine, count, totalLines int) string {
	// Build a map of line ranges to their gen.
	// The structure is: gen 0..writeGen-1 contributed lines.
	// The last 'w' wrote lines 0..loadgenTotalLines-1 at some gen.
	// Appends added lines after that at subsequent gens.
	// We reconstruct by noting that the current file state is the result
	// of the most recent 'w' plus any 'a' ops after it.
	//
	// Since we track writeGen as a simple counter and totalLines,
	// we can figure out: the base write produced lines 0..loadgenTotalLines-1,
	// then appends produced lines loadgenTotalLines..totalLines-1.
	// Each append added loadgenAppendLines lines.
	//
	// The gen for the base write is: writeGen - numAppendsSinceWrite
	// where numAppendsSinceWrite = (totalLines - loadgenTotalLines) / loadgenAppendLines

	numAppends := 0
	if totalLines > loadgenTotalLines {
		numAppends = (totalLines - loadgenTotalLines) / loadgenAppendLines
	}
	baseGen := writeGen - 1 - numAppends // gen of the most recent 'w'

	var b strings.Builder
	for i := startLine; i < startLine+count; i++ {
		var gen int
		if i < loadgenTotalLines {
			gen = baseGen
		} else {
			appendIdx := (i - loadgenTotalLines) / loadgenAppendLines
			gen = baseGen + 1 + appendIdx
		}
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", itemID, gen, i))))
		if i > startLine {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d:%d:%s", i, gen, h[:64])
	}
	return b.String()
}
