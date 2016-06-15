package client

import (
	"github.com/docker/engine-api/types"
	"golang.org/x/net/context"
)

// CheckpointCreate checkpoints a running container
func (cli *Client) CheckpointCreate(ctx context.Context, container string, options types.CriuConfig) error {
	resp, err := cli.post(ctx, "/containers/"+container+"/checkpoint", nil, options, nil)
	ensureReaderClosed(resp)

	return err
}
