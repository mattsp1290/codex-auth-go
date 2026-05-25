package codexauth

import (
	"context"
	"time"
)

// Status reads the local credential store and reports this client's login
// state. It does not perform network refreshes or delete credentials.
func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	if err := ctx.Err(); err != nil {
		return StatusInfo{}, err
	}

	configPath, err := c.Path()
	if err != nil {
		return StatusInfo{}, err
	}

	af, err := c.load()
	if err != nil {
		return StatusInfo{}, err
	}
	if af.OpenAI == nil || af.OpenAI.Refresh == "" {
		return StatusInfo{ConfigPath: configPath}, nil
	}

	creds := *af.OpenAI
	expiresAt := time.UnixMilli(creds.Expires)
	return StatusInfo{
		LoggedIn:   true,
		Stale:      !isValid(creds, time.Now),
		ExpiresAt:  expiresAt,
		AccountID:  creds.AccountID,
		ConfigPath: configPath,
	}, nil
}
