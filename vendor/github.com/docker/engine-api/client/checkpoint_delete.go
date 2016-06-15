package client

import (
	"net/url"

	"golang.org/x/net/context"
)

// CheckpointDelete deletes a checkpoint from the given container with the given name
func (cli *Client) CheckpointDelete(ctx context.Context, container string, imgDir string) error {
	query := url.Values{}
	if imgDir != "" {
		query.Set("imgDir", imgDir)
	}
	resp, err := cli.delete(ctx, "/containers/"+container+"/checkpoint", query, nil)
	ensureReaderClosed(resp)
	return err
}
