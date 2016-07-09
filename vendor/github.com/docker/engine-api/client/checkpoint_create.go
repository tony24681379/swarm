package client

import (
	"net/url"

	"github.com/docker/engine-api/types"
	"golang.org/x/net/context"
)

// CheckpointCreate checkpoints a running container
func (cli *Client) CheckpointCreate(ctx context.Context, container string, options types.CriuConfig, refreshDir string) error {
	query := url.Values{}
	if refreshDir != "" {
		query.Set("refreshDir", refreshDir)
	}
	resp, err := cli.post(ctx, "/containers/"+container+"/checkpoint", query, options, nil)
	ensureReaderClosed(resp)

	return err
}
