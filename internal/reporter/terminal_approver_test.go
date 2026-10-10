package reporter

import (
	"context"
	"strings"
	"testing"

	"github.com/jrmarcco/goaw/internal/schema"
	"github.com/jrmarcco/goaw/internal/tools"
)

func TestTerminalApproverRequestApproval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		input string

		wantDecision tools.ApprovalDecision
		wantRemember bool
	}{
		{
			name:         "approve once",
			input:        "y\n",
			wantDecision: tools.DecisionApproved,
		},
		{
			name:         "approve with leading and trailing spaces",
			input:        "  y  \n",
			wantDecision: tools.DecisionApproved,
		},
		{
			name:         "approve and remember",
			input:        "a\n",
			wantDecision: tools.DecisionApproved,
			wantRemember: true,
		},
		{
			name:         "explicit reject",
			input:        "n\n",
			wantDecision: tools.DecisionRejected,
		},
		{
			name:         "unknown input fails safe as rejected",
			input:        "whatever\n",
			wantDecision: tools.DecisionRejected,
		},
		{
			name:         "stdin exhausted fails safe as rejected",
			input:        "",
			wantDecision: tools.DecisionRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			approver := NewTerminalApprover(
				TerminalApproverWithStdin(strings.NewReader(tt.input)),
			)

			result := approver.RequestApproval(context.Background(), schema.ToolCall{ID: approvalTestCallID, Name: approvalTestToolName})

			if result.Decision != tt.wantDecision {
				t.Fatalf("Decision = %q, want %q", result.Decision, tt.wantDecision)
			}
			if result.Remember != tt.wantRemember {
				t.Fatalf("Remember = %v, want %v", result.Remember, tt.wantRemember)
			}
			if result.Decision == tools.DecisionRejected && result.Reason == "" {
				t.Fatal("拒绝结果应携带可回传模型的原因文案")
			}
		})
	}
}
