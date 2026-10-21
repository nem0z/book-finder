package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

// Copilot asks models through GitHub Copilot, with the Copilot SDK
// (https://github.com/github/copilot-sdk), billed to the GitHub account of its token. The SDK runs the
// Copilot runtime as a child process: it is built into the binary by `go tool bundler` (see README), or
// found at COPILOT_CLI_PATH. Each call is a fresh session without tools or history.
type Copilot struct {
	model string
	token string

	mu     sync.Mutex
	client *copilot.Client
	dir    string // the runtime's session state
}

// NewCopilot returns a client for a Copilot model (e.g. claude-haiku-5.5). token is a GitHub token with
// Copilot access; empty uses the account signed in to Copilot CLI or gh on this machine.
func NewCopilot(model, token string) *Copilot {
	return &Copilot{model: model, token: token}
}

// JSON implements Client. Copilot has no structured outputs, so the schema goes into the instructions and
// the answer is checked when it is decoded.
func (c *Copilot) JSON(ctx context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	client, err := c.start(ctx)
	if err != nil {
		return err
	}

	s, err := client.CreateSession(ctx, &copilot.SessionConfig{
		Model: c.model,
		SystemMessage: &copilot.SystemMessageConfig{
			Mode: "replace",
			Content: system + "\n\nAnswer with only a JSON value (" + schemaName + ") matching this JSON schema, " +
				"without any other text:\n" + string(schema),
		},
		AvailableTools:      []string{},
		OnPermissionRequest: deny,
	})
	if err != nil {
		return fmt.Errorf("copilot session: %w", err)
	}

	defer func() {
		s.Disconnect()                                                //nolint:errcheck // the session is deleted next
		client.DeleteSession(context.WithoutCancel(ctx), s.SessionID) //nolint:errcheck // only frees disk space
	}()

	ev, err := s.SendAndWait(ctx, copilot.MessageOptions{Prompt: user})
	if err != nil {
		s.Abort(context.WithoutCancel(ctx)) //nolint:errcheck // best effort, the answer is no longer wanted
		return fmt.Errorf("copilot: %w", err)
	}

	var answer string
	if ev != nil {
		if d, ok := ev.Data.(*copilot.AssistantMessageData); ok {
			answer = d.Content
		}
	}

	if err := json.Unmarshal([]byte(jsonValue(answer)), out); err != nil {
		return fmt.Errorf("%w: %w: %q", ErrBadResponse, err, answer)
	}

	return nil
}

// Close stops the Copilot runtime, if it was started.
func (c *Copilot) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return nil
	}

	err := c.client.Stop()
	c.client = nil

	return errors.Join(err, os.RemoveAll(c.dir))
}

// start starts the Copilot runtime on first use, in about a second.
func (c *Copilot) start(ctx context.Context) (*copilot.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		return c.client, nil
	}

	// The empty mode gives sessions no tools, skills or files of this machine; it needs a directory for
	// session state.
	dir, err := os.MkdirTemp("", "book-finder-copilot-")
	if err != nil {
		return nil, err
	}

	client := copilot.NewClient(&copilot.ClientOptions{
		Mode:          copilot.ModeEmpty,
		BaseDirectory: dir,
		GitHubToken:   c.token,
		LogLevel:      "error",
	})
	if err := client.Start(ctx); err != nil {
		os.RemoveAll(dir) //nolint:errcheck // still empty
		return nil, fmt.Errorf("start the Copilot runtime (built in with `go tool bundler`, or at COPILOT_CLI_PATH): %w", err)
	}

	c.client, c.dir = client, dir

	return client, nil
}

// jsonValue returns the JSON in a model's answer: the answer itself, or the content of a Markdown code
// block around it.
func jsonValue(answer string) string {
	s := strings.TrimSpace(answer)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		}

		s, _, _ = strings.Cut(rest, "```")
	}

	return strings.TrimSpace(s)
}

func deny(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
	return &rpc.PermissionDecisionDeniedNoApprovalRuleAndCouldNotRequestFromUser{}, nil
}
