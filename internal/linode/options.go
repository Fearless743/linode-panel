package linode

import "context"

// Region is one entry of GET /v4/regions.
type Region struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Country string `json:"country"`
	Status  string `json:"status"`
}

// LinodeType is one entry of GET /v4/linode/types.
type LinodeType struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Memory int    `json:"memory"`
	VCpus  int    `json:"vcpus"`
	Price  struct {
		Hourly  float64 `json:"hourly"`
		Monthly float64 `json:"monthly"`
	} `json:"price"`
}

// Image is one entry of GET /v4/images.
type Image struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ListRegions returns all Linode regions, following pagination.
func (c *Client) ListRegions(ctx context.Context) ([]Region, error) {
	return paginate[Region](c, ctx, "/regions")
}

// ListTypes returns all Linode types (plans), following pagination.
func (c *Client) ListTypes(ctx context.Context) ([]LinodeType, error) {
	return paginate[LinodeType](c, ctx, "/linode/types")
}

// ListImages returns all images (Linode distros), following pagination.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	return paginate[Image](c, ctx, "/images")
}
