package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	gogithub "github.com/google/go-github/v74/github"
)

// ListFailedHookDeliveries lists webhook deliveries with status=failure.
// repo empty means organization hook; otherwise repository hook.
func ListFailedHookDeliveries(
	ctx context.Context,
	client *gogithub.Client,
	owner string,
	repo string,
	hookID int64,
	opts *gogithub.ListCursorOptions,
) ([]*gogithub.HookDelivery, *gogithub.Response, error) {
	if client == nil {
		return nil, nil, fmt.Errorf("github client is nil")
	}
	var u string
	if repo != "" {
		u = fmt.Sprintf("repos/%s/%s/hooks/%d/deliveries", owner, repo, hookID)
	} else {
		u = fmt.Sprintf("orgs/%s/hooks/%d/deliveries", owner, hookID)
	}
	if opts == nil {
		opts = &gogithub.ListCursorOptions{PerPage: 100}
	}
	q := url.Values{}
	q.Set("status", "failure")
	if opts.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(opts.PerPage))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	u = u + "?" + q.Encode()

	req, err := client.NewRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	deliveries := []*gogithub.HookDelivery{}
	resp, err := client.Do(ctx, req, &deliveries)
	if err != nil {
		return deliveries, resp, err
	}
	return deliveries, resp, nil
}
